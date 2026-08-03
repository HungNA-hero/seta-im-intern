import type Redis from "ioredis";
import { epochAssetKey, processedEventKey } from "../cache/keys";

export interface AssetEventEnvelope {
  eventId: string;
  eventType: string;
  orgId: string;
  [key: string]: unknown;
}

const DEDUPE_AND_BUMP_SCRIPT = `
if redis.call("SET", KEYS[1], "1", "NX", "EX", ARGV[1]) then
  redis.call("INCR", KEYS[2])
  return 1
end
return 0
`;

export async function applyLifecycleEffect(
  redis: Redis,
  consumerGroup: string,
  markerTtlSeconds: number,
  event: AssetEventEnvelope,
): Promise<void> {
  if (
    !["folder.moved", "folder.deleted", "folder.restored", "metadata.deleted", "metadata.restored"].includes(
      event.eventType,
    )
  ) {
    return;
  }
  await redis.eval(
    DEDUPE_AND_BUMP_SCRIPT,
    2,
    processedEventKey(consumerGroup, event.eventId),
    epochAssetKey(event.orgId),
    String(markerTtlSeconds),
  );
}
