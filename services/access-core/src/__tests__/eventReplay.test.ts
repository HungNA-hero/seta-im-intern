import { describe, test, expect, beforeEach, afterAll } from "vitest";
import { randomUUID } from "node:crypto";

const liveRedis = process.env.ACCESS_REDIS_LIVE_TEST === "1";

describe.skipIf(!liveRedis)(
  "lifecycle event replay/reorder and the lost-publish TTL backstop (live Redis)",
  () => {
    const orgId = randomUUID();

    beforeEach(async () => {
      const { flushAllForTests } = await import("./helpers/redisTestUtils");
      await flushAllForTests();
    });

    afterAll(async () => {
      const { closeRedisClient } = await import("../cache/redisClient");
      await closeRedisClient();
    });

    test("redelivering the same eventId bumps the asset epoch only once", async () => {
      const { getRedisClient } = await import("../cache/redisClient");
      const { applyLifecycleEffect } = await import("../eventing/effects");
      const { getAssetEpoch } = await import("../cache/epoch");
      const redis = getRedisClient();

      const event = { eventId: randomUUID(), eventType: "folder.moved", orgId };
      await applyLifecycleEffect(redis, "cache-invalidator", 60, event);
      await applyLifecycleEffect(redis, "cache-invalidator", 60, event);

      expect(await getAssetEpoch(orgId)).toBe(1);
    });

    test("moved and deleted events for the same folder converge regardless of delivery order", async () => {
      const { getRedisClient } = await import("../cache/redisClient");
      const { applyLifecycleEffect } = await import("../eventing/effects");
      const { getAssetEpoch, bumpAssetEpoch } = await import("../cache/epoch");
      const redis = getRedisClient();

      const moved = { eventId: randomUUID(), eventType: "folder.moved", orgId };
      const deleted = { eventId: randomUUID(), eventType: "folder.deleted", orgId };

      await applyLifecycleEffect(redis, "cache-invalidator", 60, moved);
      await applyLifecycleEffect(redis, "cache-invalidator", 60, deleted);
      const forwardOrderEpoch = await getAssetEpoch(orgId);

      // Reset and apply in reverse order with fresh eventIds.
      await redis.del(`epoch:asset:${orgId}`);
      const movedAgain = { eventId: randomUUID(), eventType: "folder.moved", orgId };
      const deletedAgain = { eventId: randomUUID(), eventType: "folder.deleted", orgId };
      await applyLifecycleEffect(redis, "cache-invalidator", 60, deletedAgain);
      await applyLifecycleEffect(redis, "cache-invalidator", 60, movedAgain);
      const reverseOrderEpoch = await getAssetEpoch(orgId);

      expect(reverseOrderEpoch).toBe(forwardOrderEpoch);
      void bumpAssetEpoch; // sanity: import resolves
    });

    test("decision/fact cache entries never exceed the hard 4s TTL", async () => {
      const { writeDecision } = await import("../cache/decisionCache");
      const { getRedisClient } = await import("../cache/redisClient");
      const redis = getRedisClient();

      const key = `authz:${orgId}:av0:uv0:rv000000000000:read:folder:${randomUUID()}`;
      await writeDecision(key, { allowed: true, reason: null });

      const pttl = await redis.pttl(key);
      expect(pttl).toBeGreaterThan(0);
      expect(pttl).toBeLessThanOrEqual(4000);
    });
  },
);
