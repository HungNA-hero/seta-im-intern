import { withFailOpen } from "./failOpen";
import { epochAssetKey, epochRoleKey, epochUserKey } from "./keys";
import { incrementCounter } from "./metrics";

/**
 * Reads the current epoch value for a key. Missing keys read as `0`, the
 * same value a never-bumped counter would have; a Redis failure is
 * fail-open and also reads as `0` (treat as a cache miss, never as an
 * error that blocks the caller).
 */
async function readEpoch(key: string): Promise<number> {
  return withFailOpen(async (redis) => {
    const raw = await redis.get(key);
    const parsed = raw == null ? 0 : Number(raw);
    return Number.isFinite(parsed) ? parsed : 0;
  }, 0);
}

/**
 * Best-effort monotonic bump. Failures are swallowed (fail-open): the
 * caller's database commit already succeeded, and a lost bump is bounded
 * by the hard cache TTL, not retried here.
 */
async function bumpEpoch(key: string): Promise<void> {
  const bumped = await withFailOpen(async (redis) => {
    await redis.incr(key);
    return true;
  }, false);
  if (bumped) {
    incrementCounter("epoch_bump");
  } else {
    incrementCounter("lost_publish");
  }
}

export function getUserEpoch(orgId: string, userId: string): Promise<number> {
  return readEpoch(epochUserKey(orgId, userId));
}

export function getRoleEpoch(orgId: string, roleId: string): Promise<number> {
  return readEpoch(epochRoleKey(orgId, roleId));
}

export async function getRoleEpochs(orgId: string, roleIds: string[]): Promise<number[]> {
  return Promise.all(roleIds.map((roleId) => getRoleEpoch(orgId, roleId)));
}

export function getAssetEpoch(orgId: string): Promise<number> {
  return readEpoch(epochAssetKey(orgId));
}

export function bumpUserEpoch(orgId: string, userId: string): Promise<void> {
  return bumpEpoch(epochUserKey(orgId, userId));
}

export function bumpRoleEpoch(orgId: string, roleId: string): Promise<void> {
  return bumpEpoch(epochRoleKey(orgId, roleId));
}

export function bumpAssetEpoch(orgId: string): Promise<void> {
  return bumpEpoch(epochAssetKey(orgId));
}
