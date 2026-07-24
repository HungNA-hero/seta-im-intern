import { withFailOpen } from "./failOpen";
import { deserializeValue, serializeValue } from "./keys";
import { incrementCounter } from "./metrics";
import { cacheTtlConfig, jitteredTtlMs as sharedJitteredTtlMs } from "./config";

function jitteredTtlMs(): number {
  return sharedJitteredTtlMs(
    cacheTtlConfig.decisionMaxTtlMs,
    cacheTtlConfig.decisionMaxDownwardJitterMs,
  );
}

export interface CachedDecision {
  allowed: boolean;
  reason: string | null;
}

export async function readDecision(key: string): Promise<CachedDecision | undefined> {
  const cached = await withFailOpen<CachedDecision | undefined>(
    async (redis) => deserializeValue<CachedDecision>(await redis.get(key)) ?? undefined,
    undefined,
    () => incrementCounter("decision_bypass"),
  );
  if (cached !== undefined) {
    incrementCounter("decision_hit");
    return cached;
  }
  incrementCounter("decision_miss");
  return undefined;
}

/**
 * Writes a decisive outcome only. Callers must not invoke this for a
 * decision that resulted from a transient dependency failure (INV-6) —
 * decision.ts satisfies this by only reaching the write path when
 * decideAllowedResources resolves without throwing.
 */
export async function writeDecision(key: string, decision: CachedDecision): Promise<void> {
  await withFailOpen(async (redis) => {
    await redis.set(key, serializeValue(decision), "PX", jitteredTtlMs());
    return null;
  }, null);
}
