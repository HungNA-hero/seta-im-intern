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

export interface AssetBreakerDependencies {
  fetch(url: string, init: RequestInit): Promise<Response>;
  recordMetric(name: Parameters<typeof incrementCounter>[0]): void;
  log(level: "error" | "warn", event: string, fields?: Record<string, unknown>): void;
  createDependencyError(): Error;
  setTimer(callback: () => void, delayMs: number): NodeJS.Timeout;
  clearTimer(timer: NodeJS.Timeout): void;
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

function writeBreakerEvent(level: "error" | "warn", event: string, fields: Record<string, unknown> = {}): void {
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

async function fetchWithDeadlineUsing(
  url: string,
  init: RequestInit,
  dependencies: AssetBreakerDependencies,
): Promise<Response> {
  const controller = new AbortController();
  const timeout = dependencies.setTimer(() => controller.abort(), ASSET_FETCH_TIMEOUT_MS);
  try {
    const response = await dependencies.fetch(url, {
      ...init,
      signal: controller.signal,
    });
    if (typeof (response as Response).arrayBuffer !== "function") {
      return response;
    }
    const body = await response.arrayBuffer();
    return new Response(NULL_BODY_STATUSES.has(response.status) && body.byteLength === 0 ? null : body, {
      status: response.status,
      statusText: response.statusText,
      headers: response.headers,
    });
  } finally {
    dependencies.clearTimer(timeout);
  }
}

interface CapacityRejectionReporter {
  record(): void;
  disable(): void;
}

function createCapacityRejectionReporter(dependencies: AssetBreakerDependencies): CapacityRejectionReporter {
  let capacityRejectedCount = 0;
  let capacityLogTimer: NodeJS.Timeout | undefined;
  let disabled = false;

  return {
    record(): void {
      dependencies.recordMetric("asset_breaker_capacity_rejected");
      if (disabled) return;
      capacityRejectedCount += 1;
      if (capacityLogTimer) return;
      capacityLogTimer = dependencies.setTimer(() => {
        capacityLogTimer = undefined;
        if (disabled) return;
        const rejectedCount = capacityRejectedCount;
        capacityRejectedCount = 0;
        dependencies.log("warn", "asset_breaker_capacity_rejected_summary", {
          rejectedCount,
          windowMs: CAPACITY_LOG_WINDOW_MS,
        });
      }, CAPACITY_LOG_WINDOW_MS);
      capacityLogTimer.unref();
    },
    disable(): void {
      disabled = true;
      if (capacityLogTimer) {
        dependencies.clearTimer(capacityLogTimer);
        capacityLogTimer = undefined;
      }
      capacityRejectedCount = 0;
    },
  };
}

function createController(options: AssetBreakerOptions, dependencies: AssetBreakerDependencies): AssetBreakerHarness {
  let inFlight = 0;
  const capacityReporter = createCapacityRejectionReporter(dependencies);

  const action = async (url: string, init: RequestInit): Promise<Response> => {
    const response = await fetchWithDeadlineUsing(url, init, dependencies);
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
    dependencies.recordMetric("asset_breaker_open");
    dependencies.log("error", "asset_breaker_open");
  });
  breaker.on("halfOpen", () => {
    dependencies.recordMetric("asset_breaker_half_open");
    dependencies.log("warn", "asset_breaker_half_open");
  });
  breaker.on("close", () => {
    dependencies.recordMetric("asset_breaker_close");
    dependencies.log("warn", "asset_breaker_close");
  });
  breaker.on("reject", () => dependencies.recordMetric("asset_breaker_reject"));

  const breakerOpenError = (): Error => dependencies.createDependencyError();

  const rejectAsBreakerOpen = (): never => {
    dependencies.recordMetric("asset_breaker_reject");
    throw breakerOpenError();
  };

  const checkAdmission = (): void => {
    if (breaker.opened) {
      rejectAsBreakerOpen();
    }
    if (breaker.halfOpen && !breaker.pendingClose) {
      rejectAsBreakerOpen();
    }
    if (inFlight >= options.capacity) {
      capacityReporter.record();
      throw dependencies.createDependencyError();
    }
  };

  const normalizeBreakerError = (error: unknown): Response | never => {
    if (error instanceof AssetServerResponseError) {
      return error.response;
    }
    if (error instanceof Error && (error as Error & { code?: string }).code === "EOPENBREAKER") {
      throw breakerOpenError();
    }
    throw error;
  };

  return {
    async fire(url: string, init: RequestInit): Promise<Response> {
      if (!options.enabled) {
        return await fetchWithDeadlineUsing(url, init, dependencies);
      }

      checkAdmission();
      inFlight += 1;
      try {
        return await breaker.fire(url, init);
      } catch (error) {
        return normalizeBreakerError(error);
      } finally {
        inFlight -= 1;
      }
    },
    snapshot(): AssetBreakerSnapshot {
      return {
        state: breaker.halfOpen ? "halfOpen" : breaker.opened ? "open" : "closed",
        inFlight,
        stats: breaker.stats,
      };
    },
    disableCapacityLogging: () => capacityReporter.disable(),
    shutdown(): void {
      capacityReporter.disable();
      breaker.shutdown();
    },
  };
}

const defaultDependencies: AssetBreakerDependencies = {
  fetch: (url, init) => fetch(url, init),
  recordMetric: incrementCounter,
  log: writeBreakerEvent,
  createDependencyError: () => internalDependencyError(getRequestCorrelation()?.traceId),
  setTimer: (callback, delayMs) => setTimeout(callback, delayMs),
  clearTimer: (timer) => clearTimeout(timer),
};

function configuredOptions(): AssetBreakerOptions {
  return config.assetBreaker ?? DEFAULT_BREAKER_OPTIONS;
}

let assetBreaker = createController(configuredOptions(), defaultDependencies);

export function createAssetBreaker(
  options: AssetBreakerOptions,
  dependencies: AssetBreakerDependencies,
): AssetBreakerHarness {
  return createController(options, dependencies);
}

export function createAssetBreakerForTests(options: AssetBreakerOptions): AssetBreakerHarness {
  return createController(options, defaultDependencies);
}

export function fireAssetRequest(url: string, init: RequestInit): Promise<Response> {
  return assetBreaker.fire(url, init);
}

export function getAssetBreakerSnapshot(): AssetBreakerSnapshot {
  return assetBreaker.snapshot();
}

export function resetAssetBreakerForTests(): void {
  shutdownAssetBreaker();
  assetBreaker = createController(configuredOptions(), defaultDependencies);
}

export function shutdownAssetBreaker(): void {
  assetBreaker.shutdown();
}

export function disableAssetBreakerCapacityLog(): void {
  assetBreaker.disableCapacityLogging();
}
