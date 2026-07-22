import { getRedisClient } from "../../cache/redisClient";

export async function flushAllForTests(): Promise<void> {
  const redis = getRedisClient();
  await redis.flushdb();
}
