import type Redis from "ioredis";
import { epochAssetKey, processedEventKey } from "../cache/keys";

export interface AssetEventEnvelope {
  eventId: string;
  eventType: string;
  orgId: string;
  [key: string]: unknown;
}

// KEYS[1] = processed marker key, KEYS[2] = epoch:asset key
// ARGV[1] = marker TTL seconds
// Atomically dedupes on the marker and bumps the epoch only for a new marker.
// The bump is a monotonic INCR, so replays and out-of-order delivery of
// `folder.moved`/`folder.deleted` for the same folder converge to the same
// final state without any targeted key deletion.
const DEDUPE_AND_BUMP_SCRIPT = `
if redis.call("SET", KEYS[1], "1", "NX", "EX", ARGV[1]) then
  redis.call("INCR", KEYS[2])
  return 1
end
return 0
`;

/**
 * Maps a lifecycle event to its cache effect. `folder.moved` and both
 * `folder.deleted` origins (synchronous delete and the deletion job's
 * `succeeded` transition) all bump the same org-wide asset epoch, since
 * neither carries a subtree id list to target a narrower invalidation.
 */
export async function applyLifecycleEffect(
  redis: Redis,
  consumerGroup: string,
  markerTtlSeconds: number,
  event: AssetEventEnvelope,
): Promise<void> {
  if (event.eventType !== "folder.moved" && event.eventType !== "folder.deleted") return;
  await redis.eval(
    DEDUPE_AND_BUMP_SCRIPT,
    2,
    processedEventKey(consumerGroup, event.eventId),
    epochAssetKey(event.orgId),
    String(markerTtlSeconds),
  );
}
