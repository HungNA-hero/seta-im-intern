import Redis from "ioredis";
import { config } from "../config";

let client: Redis | null = null;

export function getRedisClient(): Redis {
  if (!client) {
    client = new Redis({
      host: config.redis.host,
      port: config.redis.port,
      password: config.redis.password,
      db: config.redis.db,
      connectTimeout: config.redis.connectTimeoutMs,
      commandTimeout: config.redis.commandTimeoutMs,
      enableOfflineQueue: true,
      lazyConnect: true,
      maxRetriesPerRequest: 0,
      retryStrategy: () => null,
    });
    client.on("error", () => undefined);
  }
  return client;
}

export async function closeRedisClient(): Promise<void> {
  const active = client;
  client = null;
  if (!active) return;
  try {
    await active.quit();
  } catch {
    active.disconnect(false);
  }
}

export function setRedisClientForTests(redis: Redis | null): void {
  client = redis;
}
