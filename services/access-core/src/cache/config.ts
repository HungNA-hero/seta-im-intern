const HARD_MAX_TTL_MS = 4000;

function boundedTtlMs(envVar: string, fallback: number): number {
  const raw = process.env[envVar];
  const parsed = raw ? Number(raw) : NaN;
  const value = Number.isFinite(parsed) && parsed > 0 ? parsed : fallback;
  // The 4s cache TTL is the sole correctness backstop for a lost post-commit
  // epoch bump/publish (see plan.md "Complexity Tracking"); no tuning is
  // allowed to exceed it without reintroducing durable delivery.
  return Math.min(value, HARD_MAX_TTL_MS);
}

/**
 * Decision/fact cache TTL tuning, sourced from env with a hard ceiling.
 * Jitter is subtracted from the TTL (downward-only, per FR-012) so no
 * entry can ever outlive `HARD_MAX_TTL_MS`.
 */
export const cacheTtlConfig = {
  decisionMaxTtlMs: boundedTtlMs("ACCESS_CACHE_DECISION_TTL_MS", HARD_MAX_TTL_MS),
  decisionMaxDownwardJitterMs: boundedTtlMs("ACCESS_CACHE_DECISION_JITTER_MS", 500),
  factMaxTtlMs: boundedTtlMs("ACCESS_CACHE_FACT_TTL_MS", HARD_MAX_TTL_MS),
  factMaxDownwardJitterMs: boundedTtlMs("ACCESS_CACHE_FACT_JITTER_MS", 500),
};

export function jitteredTtlMs(maxTtlMs: number, maxDownwardJitterMs: number): number {
  const safeJitterMs = Math.min(maxDownwardJitterMs, Math.max(0, maxTtlMs - 1));
  return maxTtlMs - Math.floor(Math.random() * safeJitterMs);
}
