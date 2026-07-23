import { randomUUID } from "node:crypto";
import type Redis from "ioredis";
import { getRedisClient } from "../cache/redisClient";
import { incrementCounter } from "../cache/metrics";
import { applyLifecycleEffect, type AssetEventEnvelope } from "./effects";

export const STREAM_KEY = "stream:asset-events";
export const CONSUMER_GROUP = "cache-invalidator";
export const DLQ_KEY = `${STREAM_KEY}:dlq`;

const CONSUMER_NAME = `access-core-${process.pid}-${randomUUID().slice(0, 8)}`;
const READ_COUNT = 20;
const READ_BLOCK_MS = 5000;
const CLAIM_IDLE_MS = 30_000;
const MAX_DELIVERIES = 5;
const PROCESSED_MARKER_TTL_SECONDS = 60;

function parseEnvelope(fields: string[]): AssetEventEnvelope | null {
  const record: Record<string, string> = {};
  for (let i = 0; i < fields.length; i += 2) {
    record[fields[i]] = fields[i + 1];
  }
  const raw = record.payload;
  if (!raw) return null;
  try {
    const parsed = JSON.parse(raw) as AssetEventEnvelope;
    if (typeof parsed.eventId !== "string" || typeof parsed.orgId !== "string") return null;
    return parsed;
  } catch {
    return null;
  }
}

export async function ensureConsumerGroup(redis: Redis): Promise<void> {
  try {
    await redis.xgroup("CREATE", STREAM_KEY, CONSUMER_GROUP, "0", "MKSTREAM");
  } catch (error) {
    if (error instanceof Error && error.message.includes("BUSYGROUP")) return;
    throw error;
  }
}

async function applyEffect(redis: Redis, event: AssetEventEnvelope): Promise<void> {
  await applyLifecycleEffect(redis, CONSUMER_GROUP, PROCESSED_MARKER_TTL_SECONDS, event);
}

/**
 * Emits a structured, alert-level log line. There is no external alert
 * integration in this repo (see CLAUDE.md) — log-based alerting on this
 * `level: "error"` event is the alert surface for FR-013's dead-letter
 * requirement, matching the JSON logging convention used elsewhere
 * (see authz/trainerAdmin.ts).
 */
function alertDlqEntry(messageId: string, event: AssetEventEnvelope | null): void {
  process.stderr.write(
    `${JSON.stringify({
      level: "error",
      service: "access-core",
      event: "cache_invalidator_dlq",
      messageId,
      eventId: event?.eventId,
      eventType: event?.eventType,
      orgId: event?.orgId,
      timestamp: new Date().toISOString(),
    })}\n`,
  );
}

async function deadLetter(redis: Redis, messageId: string, fields: string[]): Promise<void> {
  await redis.xadd(DLQ_KEY, "*", ...fields, "originalId", messageId);
  await redis.xack(STREAM_KEY, CONSUMER_GROUP, messageId);
  incrementCounter("invalidation_dlq");
  alertDlqEntry(messageId, parseEnvelope(fields));
}

async function processMessage(redis: Redis, messageId: string, fields: string[]): Promise<void> {
  const event = parseEnvelope(fields);
  if (event) {
    await applyEffect(redis, event);
  }
  await redis.xack(STREAM_KEY, CONSUMER_GROUP, messageId);
}

/**
 * Reclaims messages left pending past the visibility timeout (consumer
 * crashed mid-processing) and either retries or, past MAX_DELIVERIES,
 * dead-letters them.
 */
export async function reclaimStalePending(redis: Redis): Promise<void> {
  const pending = await redis.xpending(
    STREAM_KEY,
    CONSUMER_GROUP,
    "IDLE",
    CLAIM_IDLE_MS,
    "-",
    "+",
    READ_COUNT,
  );
  if (!Array.isArray(pending) || pending.length === 0) return;

  for (const entry of pending as unknown as [string, string, number, number][]) {
    const [messageId, , , deliveryCount] = entry;
    const claimed = await redis.xclaim(
      STREAM_KEY,
      CONSUMER_GROUP,
      CONSUMER_NAME,
      CLAIM_IDLE_MS,
      messageId,
    );
    const claimedEntry = (claimed as unknown as [string, string[]][])[0];
    if (!claimedEntry) continue;
    const [, fields] = claimedEntry;
    if (deliveryCount >= MAX_DELIVERIES) {
      await deadLetter(redis, messageId, fields);
      continue;
    }
    await processMessage(redis, messageId, fields);
  }
}

/**
 * Reads and processes one batch from the consumer group. Exported
 * separately from the run loop so tests can drive it deterministically.
 */
export async function processBatch(redis: Redis): Promise<number> {
  const response = await redis.xreadgroup(
    "GROUP",
    CONSUMER_GROUP,
    CONSUMER_NAME,
    "COUNT",
    READ_COUNT,
    "BLOCK",
    READ_BLOCK_MS,
    "STREAMS",
    STREAM_KEY,
    ">",
  );
  if (!response) return 0;
  const [[, messages]] = response as unknown as [string, [string, string[]][]][];
  for (const [messageId, fields] of messages) {
    await processMessage(redis, messageId, fields);
  }
  return messages.length;
}

let running = false;

/**
 * Starts the cache-invalidator consumer loop. Fail-open: any error reading
 * or processing a batch is logged and the loop continues rather than
 * crashing the process — a stalled consumer is bounded by the decision/fact
 * TTL, not by keeping the service up.
 */
const ERROR_BACKOFF_MS = 1000;

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

export function startCacheInvalidator(): { stop: () => void } {
  running = true;
  const redis = getRedisClient();

  void (async () => {
    await ensureConsumerGroup(redis).catch(() => undefined);
    while (running) {
      try {
        await reclaimStalePending(redis);
        await processBatch(redis);
      } catch {
        // A rejected Redis command (e.g. the connection is down) resolves
        // near-instantly, not after READ_BLOCK_MS — without this backoff
        // the loop would spin as fast as the event loop allows, starving
        // request handling for the whole outage. Bounded by cache TTL either way.
        await delay(ERROR_BACKOFF_MS);
      }
    }
  })();

  return {
    stop: () => {
      running = false;
    },
  };
}
