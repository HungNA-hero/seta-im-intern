import Redis from "ioredis";
import { config } from "../config";

let client: Redis | null = null;
let consumerClient: Redis | null = null;

function createConnection(commandTimeout?: number): Redis {
  const redis = new Redis({
    host: config.redis.host,
    port: config.redis.port,
    password: config.redis.password,
    db: config.redis.db,
    connectTimeout: config.redis.connectTimeoutMs,
    commandTimeout,
    enableOfflineQueue: true,
    lazyConnect: true,
    maxRetriesPerRequest: 0,
    retryStrategy: () => null,
  });
  redis.on("error", () => undefined);
  return redis;
}

export function getRedisClient(): Redis {
  if (!client) {
    client = createConnection(config.redis.commandTimeoutMs);
  }
  return client;
}

export function getRedisConsumerClient(): Redis {
  if (!consumerClient) {
    consumerClient = createConnection();
  }
  return consumerClient;
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

export async function closeRedisConsumerClient(): Promise<void> {
  const active = consumerClient;
  consumerClient = null;
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
