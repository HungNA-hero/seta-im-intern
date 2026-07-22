import { createHash } from "node:crypto";

/**
 * Combines a user's current role epochs into one short, order-independent
 * token embedded in decision keys. Sorting first means the same role set
 * always hashes the same way regardless of fetch order.
 */
export function hashRoleEpochs(roleEpochs: number[]): string {
  const normalized = [...roleEpochs].sort((a, b) => a - b).join(",");
  return createHash("sha1").update(normalized).digest("hex").slice(0, 12);
}

export interface DecisionKeyParams {
  orgId: string;
  assetEpoch: number;
  userEpoch: number;
  roleEpochsHash: string;
  action: string;
  resourceType: string;
  resourceId: string;
}

export function decisionKey(params: DecisionKeyParams): string {
  const { orgId, assetEpoch, userEpoch, roleEpochsHash, action, resourceType, resourceId } =
    params;
  return `authz:${orgId}:av${assetEpoch}:uv${userEpoch}:rv${roleEpochsHash}:${action}:${resourceType}:${resourceId}`;
}

export function factFolderKey(orgId: string, assetEpoch: number, folderId: string): string {
  return `fact:${orgId}:av${assetEpoch}:folder:${folderId}`;
}

export function factItemKey(orgId: string, assetEpoch: number, itemId: string): string {
  return `fact:${orgId}:av${assetEpoch}:item:${itemId}`;
}

export function epochUserKey(orgId: string, userId: string): string {
  return `epoch:user:${orgId}:${userId}`;
}

export function epochRoleKey(orgId: string, roleId: string): string {
  return `epoch:role:${orgId}:${roleId}`;
}

export function epochAssetKey(orgId: string): string {
  return `epoch:asset:${orgId}`;
}

export function processedEventKey(consumerName: string, eventId: string): string {
  return `processed:${consumerName}:${eventId}`;
}

/**
 * Serializes a cache value to a JSON string, safe to store in Redis.
 */
export function serializeValue<T>(value: T): string {
  return JSON.stringify(value);
}

/**
 * Deserializes a cache value written by `serializeValue`. Returns `null` on
 * malformed input instead of throwing, so a corrupted/foreign entry is
 * treated as a miss rather than crashing the read path.
 */
export function deserializeValue<T>(raw: string | null | undefined): T | null {
  if (raw == null) return null;
  try {
    return JSON.parse(raw) as T;
  } catch {
    return null;
  }
}
