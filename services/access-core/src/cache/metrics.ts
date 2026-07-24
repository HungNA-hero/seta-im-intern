type Counter =
  | "decision_hit"
  | "decision_miss"
  | "decision_bypass"
  | "fact_hit"
  | "fact_miss"
  | "fact_bypass"
  | "epoch_bump"
  | "single_flight_coalesced"
  | "lost_publish"
  | "invalidation_dlq";

const counters: Record<Counter, number> = {
  decision_hit: 0,
  decision_miss: 0,
  decision_bypass: 0,
  fact_hit: 0,
  fact_miss: 0,
  fact_bypass: 0,
  epoch_bump: 0,
  single_flight_coalesced: 0,
  lost_publish: 0,
  invalidation_dlq: 0,
};

const invalidationLatenciesMs: number[] = [];
const MAX_LATENCY_SAMPLES = 1000;

export function incrementCounter(name: Counter): void {
  counters[name] += 1;
}

/**
 * Records the time from a mutation's commit (or a deletion job's
 * `succeeded` transition) to the epoch bump becoming visible, for the
 * end-to-end invalidation SLO (< 5s).
 */
export function recordInvalidationLatencyMs(latencyMs: number): void {
  invalidationLatenciesMs.push(latencyMs);
  if (invalidationLatenciesMs.length > MAX_LATENCY_SAMPLES) {
    invalidationLatenciesMs.shift();
  }
}

export function getMetricsSnapshotForTests(): {
  counters: Readonly<Record<Counter, number>>;
  invalidationLatenciesMs: readonly number[];
} {
  return { counters: { ...counters }, invalidationLatenciesMs: [...invalidationLatenciesMs] };
}

export function resetMetricsForTests(): void {
  (Object.keys(counters) as Counter[]).forEach((key) => {
    counters[key] = 0;
  });
  invalidationLatenciesMs.length = 0;
}
