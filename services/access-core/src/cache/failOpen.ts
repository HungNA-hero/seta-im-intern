import { getRedisClient } from "./redisClient";

const FAILURE_THRESHOLD = 5;
const OPEN_DURATION_MS = 2000;

let consecutiveFailures = 0;
let circuitOpenUntil = 0;

function circuitOpen(): boolean {
  return circuitOpenUntil > Date.now();
}

function recordSuccess(): void {
  consecutiveFailures = 0;
  circuitOpenUntil = 0;
}

function recordFailure(): void {
  consecutiveFailures += 1;
  if (consecutiveFailures >= FAILURE_THRESHOLD) {
    circuitOpenUntil = Date.now() + OPEN_DURATION_MS;
  }
}

/**
 * Runs a Redis-backed operation with fail-open semantics: on timeout, error,
 * or an open circuit breaker, returns `fallback` instead of throwing or
 * blocking. Never used to decide "allow" — callers fall back to the
 * authoritative decision, never to a permissive default.
 */
export async function withFailOpen<T>(
  operation: (redis: ReturnType<typeof getRedisClient>) => Promise<T>,
  fallback: T,
  onBypass?: () => void,
): Promise<T> {
  if (circuitOpen()) {
    onBypass?.();
    return fallback;
  }
  try {
    const redis = getRedisClient();
    const result = await operation(redis);
    recordSuccess();
    return result;
  } catch {
    recordFailure();
    onBypass?.();
    return fallback;
  }
}

export function resetCircuitBreakerForTests(): void {
  consecutiveFailures = 0;
  circuitOpenUntil = 0;
}
