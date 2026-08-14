import { describe, expect, test, vi } from "vitest";
import { createMediaRateLimiter } from "../rest/mediaRateLimit";

const fixedNow = Date.UTC(2026, 7, 13, 10, 0, 30);

function limiterWith(result: unknown) {
  const redis = { eval: vi.fn().mockResolvedValue(result) };
  return {
    redis,
    limiter: createMediaRateLimiter(() => redis, {
      userLimit: 10,
      organizationLimit: 60,
      windowMs: 60_000,
      now: () => fixedNow,
    }),
  };
}

describe("media session creation rate limiter", () => {
  test("checks user 10/min and organization 60/min in one atomic Redis operation", async () => {
    const { redis, limiter } = limiterWith([1, 1, 1, 30_000]);

    await expect(limiter.consumeSessionCreation({ userId: "user-1", orgId: "org-1" })).resolves.toBeUndefined();

    expect(redis.eval).toHaveBeenCalledTimes(1);
    const [script, keyCount, userKey, orgKey, userLimit, orgLimit, ttl] = redis.eval.mock.calls[0];
    expect(String(script)).toContain("redis.call('INCR', KEYS[1])");
    expect(String(script)).toContain("redis.call('INCR', KEYS[2])");
    expect(keyCount).toBe(2);
    expect(userKey).toContain("media:session-create:user:user-1");
    expect(orgKey).toContain("media:session-create:org:org-1");
    expect([userLimit, orgLimit, ttl]).toEqual([10, 60, 60_000]);
  });

  test.each([
    ["user", [0, 10, 25, 30_000]],
    ["organization", [0, 4, 60, 30_000]],
  ])("returns MEDIA_RATE_LIMITED when the %s limit is exhausted", async (_scope, result) => {
    const { limiter } = limiterWith(result);

    await expect(limiter.consumeSessionCreation({ userId: "user-1", orgId: "org-1" })).rejects.toMatchObject({
      extensions: { code: "MEDIA_RATE_LIMITED", number: 6014, retryAfterSeconds: 30 },
    });
  });

  test("fails closed when Redis is unavailable", async () => {
    const redis = { eval: vi.fn().mockRejectedValue(new Error("redis unavailable")) };
    const limiter = createMediaRateLimiter(() => redis, {
      userLimit: 10,
      organizationLimit: 60,
      windowMs: 60_000,
      now: () => fixedNow,
    });

    await expect(limiter.consumeSessionCreation({ userId: "user-1", orgId: "org-1" })).rejects.toMatchObject({
      extensions: { code: "INTERNAL_ERROR" },
    });
  });
});
