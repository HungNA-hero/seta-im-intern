import CircuitBreaker from "opossum";
import { config } from "../config";
import { incrementCounter } from "../cache/metrics";
import { internalDependencyError } from "../errors/factories";
import { getRequestCorrelation } from "../observability/requestContext";
import { ServiceName } from "../observability/serviceName";

const ASSET_FETCH_TIMEOUT_MS = 3000;
const CAPACITY_LOG_WINDOW_MS = 5000;

export interface AssetBreakerOptions {
  enabled: boolean;
  errorThresholdPercentage: number;
  volumeThreshold: number;
  resetTimeoutMs: number;
  capacity: number;
}

export interface AssetBreakerSnapshot {
  state: "closed" | "open" | "halfOpen";
  inFlight: number;
  stats: CircuitBreaker.Stats;
}

export interface AssetBreakerHarness {
  fire(url: string, init: RequestInit): Promise<Response>;
  snapshot(): AssetBreakerSnapshot;
  cancelPendingCapacityLog(): void;
  shutdown(): void;
}

const DEFAULT_BREAKER_OPTIONS: AssetBreakerOptions = {
  enabled: true,
  errorThresholdPercentage: 50,
  volumeThreshold: 10,
  resetTimeoutMs: 5000,
  capacity: 800,
};

class AssetServerResponseError extends Error {
  constructor(readonly response: Response) {
    super(`Asset Core returned HTTP ${response.status}`);
    this.name = "AssetServerResponseError";
  }
}

function writeBreakerEvent(
  level: "error" | "warn",
  event: string,
  fields: Record<string, unknown> = {},
): void {
  process.stderr.write(
    `${JSON.stringify({
      level,
      service: ServiceName.ACCESS_CORE,
      event,
      ...fields,
      timestamp: new Date().toISOString(),
    })}\n`,
  );
}

async function fetchWithDeadline(
  url: string,
  init: RequestInit,
): Promise<Response> {
  const controller = new AbortController();
  const timeout = setTimeout(
    () => controller.abort(),
    ASSET_FETCH_TIMEOUT_MS,
  );
  try {
    return await fetch(url, { ...init, signal: controller.signal });
  } finally {
    clearTimeout(timeout);
  }
}

function createController(options: AssetBreakerOptions): AssetBreakerHarness {
  let inFlight = 0;
  let capacityRejectedCount = 0;
  let capacityLogTimer: NodeJS.Timeout | undefined;

  const recordCapacityRejection = (): void => {
    incrementCounter("asset_breaker_capacity_rejected");
    capacityRejectedCount += 1;
    if (capacityLogTimer) return;
    capacityLogTimer = setTimeout(() => {
      const rejectedCount = capacityRejectedCount;
      capacityRejectedCount = 0;
      capacityLogTimer = undefined;
      writeBreakerEvent(
        "warn",
        "asset_breaker_capacity_rejected_summary",
        { rejectedCount, windowMs: CAPACITY_LOG_WINDOW_MS },
      );
    }, CAPACITY_LOG_WINDOW_MS);
    capacityLogTimer.unref();
  };

  const cancelPendingCapacityLog = (): void => {
    if (capacityLogTimer) {
      clearTimeout(capacityLogTimer);
      capacityLogTimer = undefined;
    }
    capacityRejectedCount = 0;
  };

  const action = async (url: string, init: RequestInit): Promise<Response> => {
    const response = await fetchWithDeadline(url, init);
    if (response.status >= 500) {
      throw new AssetServerResponseError(response);
    }
    return response;
  };

  const breaker = new CircuitBreaker<[string, RequestInit], Response>(action, {
    enabled: options.enabled,
    timeout: false,
    errorThresholdPercentage: options.errorThresholdPercentage,
    volumeThreshold: options.volumeThreshold,
    resetTimeout: options.resetTimeoutMs,
  });

  breaker.on("open", () => {
    incrementCounter("asset_breaker_open");
    writeBreakerEvent("error", "asset_breaker_open");
  });
  breaker.on("halfOpen", () => {
    incrementCounter("asset_breaker_half_open");
    writeBreakerEvent("warn", "asset_breaker_half_open");
  });
  breaker.on("close", () => {
    incrementCounter("asset_breaker_close");
    writeBreakerEvent("warn", "asset_breaker_close");
  });
  breaker.on("reject", () => incrementCounter("asset_breaker_reject"));

  return {
    async fire(url: string, init: RequestInit): Promise<Response> {
      if (!options.enabled) {
        return await fetchWithDeadline(url, init);
      }
      if (inFlight >= options.capacity) {
        recordCapacityRejection();
        throw internalDependencyError(getRequestCorrelation()?.traceId);
      }
      inFlight += 1;
      try {
        return await breaker.fire(url, init);
      } catch (error) {
        if (error instanceof AssetServerResponseError) {
          return error.response;
        }
        if (
          error instanceof Error &&
          (error as Error & { code?: string }).code === "EOPENBREAKER"
        ) {
          throw internalDependencyError(getRequestCorrelation()?.traceId);
        }
        throw error;
      } finally {
        inFlight -= 1;
      }
    },
    snapshot(): AssetBreakerSnapshot {
      return {
        state: breaker.halfOpen
          ? "halfOpen"
          : breaker.opened
            ? "open"
            : "closed",
        inFlight,
        stats: breaker.stats,
      };
    },
    cancelPendingCapacityLog,
    shutdown(): void {
      cancelPendingCapacityLog();
      breaker.shutdown();
    },
  };
}

function configuredOptions(): AssetBreakerOptions {
  // A few resolver unit tests replace `config` with a deliberately minimal
  // runtime mock. Production always has the validated block from config.ts.
  return config.assetBreaker ?? DEFAULT_BREAKER_OPTIONS;
}

let assetBreaker = createController(configuredOptions());

export function createAssetBreakerForTests(
  options: AssetBreakerOptions,
): AssetBreakerHarness {
  return createController(options);
}

export function fireAssetRequest(
  url: string,
  init: RequestInit,
): Promise<Response> {
  return assetBreaker.fire(url, init);
}

export function resetAssetBreakerForTests(): void {
  shutdownAssetBreaker();
  assetBreaker = createController(configuredOptions());
}

export function shutdownAssetBreaker(): void {
  assetBreaker.shutdown();
}

/**
 * Stops the pending capacity-rejection summary log without tearing down the
 * breaker itself, so no summary can fire during the request-drain phase of
 * shutdown, which runs before the breaker's own `close` target.
 */
export function cancelAssetBreakerCapacityLog(): void {
  assetBreaker.cancelPendingCapacityLog();
}
