import { withFailOpen } from "./failOpen";
import { getAssetEpoch } from "./epoch";
import { factFolderKey, factItemKey, serializeValue, deserializeValue } from "./keys";
import { incrementCounter } from "./metrics";
import { cacheTtlConfig, jitteredTtlMs as sharedJitteredTtlMs } from "./config";

function jitteredTtlMs(): number {
  return sharedJitteredTtlMs(cacheTtlConfig.factMaxTtlMs, cacheTtlConfig.factMaxDownwardJitterMs);
}

async function readThrough<T>(key: string, loader: () => Promise<T | null>): Promise<T | null> {
  const cached = await withFailOpen<T | undefined>(
    async (redis) => deserializeValue<T>(await redis.get(key)) ?? undefined,
    undefined,
    () => incrementCounter("fact_bypass"),
  );
  if (cached !== undefined) {
    incrementCounter("fact_hit");
    return cached;
  }
  incrementCounter("fact_miss");

  const value = await loader();
  if (value !== null) {
    await withFailOpen(async (redis) => {
      await redis.set(key, serializeValue(value), "PX", jitteredTtlMs());
      return null;
    }, null);
  }
  return value;
}

export interface FolderFact {
  path: string;
}

export interface ItemFact {
  folderId: string;
}

export async function readFolderFactThrough(
  orgId: string,
  folderId: string,
  loader: () => Promise<FolderFact | null>,
): Promise<FolderFact | null> {
  const assetEpoch = await getAssetEpoch(orgId);
  return readThrough(factFolderKey(orgId, assetEpoch, folderId), loader);
}

export async function readItemFactThrough(
  orgId: string,
  itemId: string,
  loader: () => Promise<ItemFact | null>,
): Promise<ItemFact | null> {
  const assetEpoch = await getAssetEpoch(orgId);
  return readThrough(factItemKey(orgId, assetEpoch, itemId), loader);
}
