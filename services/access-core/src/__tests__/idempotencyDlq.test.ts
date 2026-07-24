import { describe, test, expect, vi, beforeEach, afterEach } from "vitest";
import { randomUUID } from "node:crypto";

function fakeRedis(overrides: Record<string, unknown> = {}) {
  return {
    eval: vi.fn().mockResolvedValue(1),
    xack: vi.fn().mockResolvedValue(1),
    xadd: vi.fn().mockResolvedValue("1-0"),
    xpending: vi.fn(),
    xclaim: vi.fn(),
    ...overrides,
  } as any;
}

describe("cache-invalidator idempotency, retry, and DLQ", () => {
  let stderrSpy: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    stderrSpy = vi.spyOn(process.stderr, "write").mockImplementation(() => true);
  });

  afterEach(() => {
    stderrSpy.mockRestore();
  });

  test("the dedupe-and-bump Lua script is invoked with the marker and epoch keys for a new event", async () => {
    const { applyLifecycleEffect } = await import("../eventing/effects");
    const redis = fakeRedis();
    const orgId = randomUUID();
    const eventId = randomUUID();

    await applyLifecycleEffect(redis, "cache-invalidator", 60, {
      eventId,
      eventType: "folder.moved",
      orgId,
    });

    expect(redis.eval).toHaveBeenCalledTimes(1);
    const [, numKeys, markerKey, epochKey, ttl] = redis.eval.mock.calls[0];
    expect(numKeys).toBe(2);
    expect(markerKey).toBe(`processed:cache-invalidator:${eventId}`);
    expect(epochKey).toBe(`epoch:asset:${orgId}`);
    expect(ttl).toBe("60");
  });

  test("a pending message below the retry ceiling is retried, not dead-lettered", async () => {
    const { reclaimStalePending } = await import("../eventing/cacheInvalidator");
    const messageId = "1-0";
    const payload = JSON.stringify({ eventId: randomUUID(), eventType: "folder.moved", orgId: randomUUID() });
    const redis = fakeRedis({
      xpending: vi.fn().mockResolvedValue([[messageId, "consumer-x", 40_000, 4]]),
      xclaim: vi.fn().mockResolvedValue([[messageId, ["payload", payload]]]),
    });

    await reclaimStalePending(redis);

    expect(redis.eval).toHaveBeenCalledTimes(1);
    expect(redis.xadd).not.toHaveBeenCalled();
    expect(redis.xack).toHaveBeenCalledWith("stream:asset-events", "cache-invalidator", messageId);
  });

  test("a message past MAX_DELIVERIES (5) is dead-lettered and raises an alert log", async () => {
    const { reclaimStalePending, DLQ_KEY } = await import("../eventing/cacheInvalidator");
    const messageId = "2-0";
    const eventId = randomUUID();
    const payload = JSON.stringify({ eventId, eventType: "folder.moved", orgId: randomUUID() });
    const redis = fakeRedis({
      xpending: vi.fn().mockResolvedValue([[messageId, "consumer-x", 40_000, 5]]),
      xclaim: vi.fn().mockResolvedValue([[messageId, ["payload", payload]]]),
    });

    await reclaimStalePending(redis);

    expect(redis.eval).not.toHaveBeenCalled();
    expect(redis.xadd).toHaveBeenCalledWith(
      DLQ_KEY,
      "*",
      "payload",
      payload,
      "originalId",
      messageId,
    );
    expect(redis.xack).toHaveBeenCalledWith("stream:asset-events", "cache-invalidator", messageId);

    expect(stderrSpy).toHaveBeenCalledTimes(1);
    const logged = JSON.parse((stderrSpy.mock.calls[0][0] as string).trim());
    expect(logged).toMatchObject({
      level: "error",
      event: "cache_invalidator_dlq",
      messageId,
      eventId,
    });
  });
});
