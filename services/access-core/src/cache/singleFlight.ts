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

/**
 * Same get-or-compute-and-store shape as `singleFlight`, but against a
 * caller-supplied map instead of this module's own — used for scoping the
 * dedup lifetime to something other than "until fn settles" (e.g. a single
 * request), and entries are not cleaned up on completion.
 */
export function memoize<T>(
  memo: Map<string, Promise<unknown>>,
  key: string,
  fn: () => Promise<T>,
): Promise<T> {
  const existing = memo.get(key);
  if (existing) return existing as Promise<T>;
  const promise = fn();
  memo.set(key, promise);
  return promise;
}
