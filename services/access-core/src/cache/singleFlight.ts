import { incrementCounter } from "./metrics";

const inFlight = new Map<string, Promise<unknown>>();

/**
 * Coalesces concurrent calls sharing the same key into a single execution of
 * `fn`. Used on decision/fact cache misses so a bump-time stampede of
 * identical requests collapses into one authoritative recomputation.
 */
export function singleFlight<T>(key: string, fn: () => Promise<T>): Promise<T> {
  const existing = inFlight.get(key);
  if (existing) {
    incrementCounter("single_flight_coalesced");
    return existing as Promise<T>;
  }

  const promise = fn().finally(() => {
    inFlight.delete(key);
  });
  inFlight.set(key, promise);
  return promise;
}

export function singleFlightPendingCountForTests(): number {
  return inFlight.size;
}
