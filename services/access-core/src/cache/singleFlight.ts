import { incrementCounter } from "./metrics";

const inFlight = new Map<string, Promise<unknown>>();

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

export function memoize<T>(memo: Map<string, Promise<unknown>>, key: string, fn: () => Promise<T>): Promise<T> {
  const existing = memo.get(key);
  if (existing) return existing as Promise<T>;
  const promise = fn();
  memo.set(key, promise);
  return promise;
}
