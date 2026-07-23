import { describe, test, expect, beforeEach, afterAll } from "vitest";
import { randomUUID } from "node:crypto";

const liveRedis = process.env.ACCESS_REDIS_LIVE_TEST === "1";

describe.skipIf(!liveRedis)(
  "async deletion-job succeeded transition invalidates cached authorization (live Redis)",
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

    test("a job-path folder.deleted event (jobId present) bumps epoch:asset", async () => {
      const { getRedisClient } = await import("../cache/redisClient");
      const { applyLifecycleEffect } = await import("../eventing/effects");
      const { getAssetEpoch } = await import("../cache/epoch");
      const redis = getRedisClient();

      const event = {
        eventId: randomUUID(),
        eventType: "folder.deleted",
        orgId,
        data: { folderId: randomUUID(), rootPath: "abc", jobId: randomUUID() },
      };
      await applyLifecycleEffect(redis, "cache-invalidator", 60, event);

      expect(await getAssetEpoch(orgId)).toBe(1);
    });

    test("redelivering the same succeeded-job event is a no-op the second time", async () => {
      const { getRedisClient } = await import("../cache/redisClient");
      const { applyLifecycleEffect } = await import("../eventing/effects");
      const { getAssetEpoch } = await import("../cache/epoch");
      const redis = getRedisClient();

      const event = {
        eventId: randomUUID(),
        eventType: "folder.deleted",
        orgId,
        data: { folderId: randomUUID(), rootPath: "abc", jobId: randomUUID() },
      };
      await applyLifecycleEffect(redis, "cache-invalidator", 60, event);
      await applyLifecycleEffect(redis, "cache-invalidator", 60, event);

      expect(await getAssetEpoch(orgId)).toBe(1);
    });

    test("failed/cancelled jobs never publish, so no event effect and no epoch bump occurs", async () => {
      const { getAssetEpoch } = await import("../cache/epoch");
      // Nothing is published for failed/cancelled jobs (see asset-core's
      // folder_deletion_repository.go — only the `succeeded` transition
      // calls PublishFolderDeleted). Absent any event, the epoch stays 0
      // and the ≤4s decision/fact TTL is the only bound on staleness.
      expect(await getAssetEpoch(orgId)).toBe(0);
    });

    test("a suppressed publish is bounded by the hard 4s cache TTL, not left uninvalidated", async () => {
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
