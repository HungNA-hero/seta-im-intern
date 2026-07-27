import CircuitBreaker from "opossum";
import { ASSET_FETCH_TIMEOUT_MS, config } from "../config";
import { incrementCounter } from "../cache/metrics";
import { internalDependencyError } from "../errors/factories";
import { getRequestCorrelation } from "../observability/requestContext";
import { ServiceName } from "../observability/serviceName";

const CAPACITY_LOG_WINDOW_MS = 5000;
const NULL_BODY_STATUSES = new Set([101, 103, 204, 205, 304]);

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
  disableCapacityLogging(): void;
  shutdown(): void;
}

const DEFAULT_BREAKER_OPTIONS: AssetBreakerOptions = {
  enabled: true,
  errorThresholdPercentage: 50,
  volumeThreshold: 10,
  resetTimeoutMs: 5000,
  capacity: 50,
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
  const timeout = setTimeout(() => controller.abort(), ASSET_FETCH_TIMEOUT_MS);
  try {
    const response = await fetch(url, { ...init, signal: controller.signal });
    if (typeof (response as Response).arrayBuffer !== "function") {
      return response;
    }
    const body = await response.arrayBuffer();
    return new Response(
      NULL_BODY_STATUSES.has(response.status) && body.byteLength === 0
        ? null
        : body,
      {
        status: response.status,
        statusText: response.statusText,
        headers: response.headers,
      },
    );
  } finally {
    clearTimeout(timeout);
  }
}

function createController(options: AssetBreakerOptions): AssetBreakerHarness {
  let inFlight = 0;
  let capacityRejectedCount = 0;
  let capacityLogTimer: NodeJS.Timeout | undefined;
  let capacityLoggingDisabled = false;

  const recordCapacityRejection = (): void => {
    incrementCounter("asset_breaker_capacity_rejected");
    if (capacityLoggingDisabled) return;
    capacityRejectedCount += 1;
    if (capacityLogTimer) return;
    capacityLogTimer = setTimeout(() => {
      capacityLogTimer = undefined;
      if (capacityLoggingDisabled) return;
      const rejectedCount = capacityRejectedCount;
      capacityRejectedCount = 0;
      writeBreakerEvent("warn", "asset_breaker_capacity_rejected_summary", {
        rejectedCount,
        windowMs: CAPACITY_LOG_WINDOW_MS,
      });
    }, CAPACITY_LOG_WINDOW_MS);
    capacityLogTimer.unref();
  };

  const disableCapacityLogging = (): void => {
    capacityLoggingDisabled = true;
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

  let halfOpenProbeClaimed = false;

  breaker.on("open", () => {
    halfOpenProbeClaimed = false;
    incrementCounter("asset_breaker_open");
    writeBreakerEvent("error", "asset_breaker_open");
  });
  breaker.on("halfOpen", () => {
    halfOpenProbeClaimed = false;
    incrementCounter("asset_breaker_half_open");
    writeBreakerEvent("warn", "asset_breaker_half_open");
  });
  breaker.on("close", () => {
    halfOpenProbeClaimed = false;
    incrementCounter("asset_breaker_close");
    writeBreakerEvent("warn", "asset_breaker_close");
  });
  breaker.on("reject", () => incrementCounter("asset_breaker_reject"));

  const breakerOpenError = (): Error =>
    internalDependencyError(getRequestCorrelation()?.traceId);

  const rejectAsBreakerOpen = (): never => {
    incrementCounter("asset_breaker_reject");
    throw breakerOpenError();
  };

  return {
    async fire(url: string, init: RequestInit): Promise<Response> {
      if (!options.enabled) {
        return await fetchWithDeadline(url, init);
      }

      if (breaker.opened) {
        rejectAsBreakerOpen();
      }
      if (breaker.halfOpen) {
        if (halfOpenProbeClaimed) {
          rejectAsBreakerOpen();
        }
        halfOpenProbeClaimed = true;
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
          throw breakerOpenError();
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
    disableCapacityLogging,
    shutdown(): void {
      disableCapacityLogging();
      breaker.shutdown();
    },
  };
}

function configuredOptions(): AssetBreakerOptions {
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

export function disableAssetBreakerCapacityLog(): void {
  assetBreaker.disableCapacityLogging();
}
