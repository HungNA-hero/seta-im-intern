import { GraphQLError } from "graphql";
import { getRedisClient } from "../cache/redisClient";
import { config } from "../config";
import { getErrorDefinition } from "../errors/errorCodes";
import { internalDependencyError } from "../errors/factories";
import { getRequestCorrelation } from "../observability/requestContext";

interface RedisEval {
  eval(script: string, numberOfKeys: number, ...args: Array<string | number>): Promise<unknown>;
}

export interface MediaRateLimitOptions {
  userLimit: number;
  organizationLimit: number;
  windowMs: number;
  now(): number;
}

const consumeBothLimits = `
local user_count = tonumber(redis.call('GET', KEYS[1]) or '0')
local org_count = tonumber(redis.call('GET', KEYS[2]) or '0')
local user_limit = tonumber(ARGV[1])
local org_limit = tonumber(ARGV[2])
local ttl_ms = tonumber(ARGV[3])

if user_count >= user_limit or org_count >= org_limit then
  local user_ttl = redis.call('PTTL', KEYS[1])
  local org_ttl = redis.call('PTTL', KEYS[2])
  local retry_ms = math.max(user_ttl, org_ttl, 1)
  return {0, user_count, org_count, retry_ms}
end

user_count = redis.call('INCR', KEYS[1])
if user_count == 1 then redis.call('PEXPIRE', KEYS[1], ttl_ms) end
org_count = redis.call('INCR', KEYS[2])
if org_count == 1 then redis.call('PEXPIRE', KEYS[2], ttl_ms) end
local retry_ms = math.max(redis.call('PTTL', KEYS[1]), redis.call('PTTL', KEYS[2]), 1)
return {1, user_count, org_count, retry_ms}
`;

export function createMediaRateLimiter(resolveRedis: () => RedisEval, options: MediaRateLimitOptions) {
  return {
    async consumeSessionCreation(input: { userId: string; orgId: string }): Promise<void> {
      const bucket = Math.floor(options.now() / options.windowMs);
      const userKey = `media:session-create:user:${input.userId}:${bucket}`;
      const orgKey = `media:session-create:org:${input.orgId}:${bucket}`;
      let raw: unknown;
      try {
        raw = await resolveRedis().eval(
          consumeBothLimits,
          2,
          userKey,
          orgKey,
          options.userLimit,
          options.organizationLimit,
          options.windowMs,
        );
      } catch {
        throw internalDependencyError(getRequestCorrelation()?.traceId);
      }
      if (!Array.isArray(raw) || raw.length < 4 || raw.some((value) => typeof value !== "number")) {
        throw internalDependencyError(getRequestCorrelation()?.traceId);
      }
      const [allowed, , , retryMs] = raw as number[];
      if (allowed !== 1) {
        const definition = getErrorDefinition("MEDIA_RATE_LIMITED");
        throw new GraphQLError(definition.message, {
          extensions: {
            code: definition.code,
            number: definition.number,
            retryAfterSeconds: Math.max(1, Math.ceil(retryMs / 1000)),
          },
        });
      }
    },
  };
}

export const mediaRateLimiter = createMediaRateLimiter(getRedisClient, {
  userLimit: config.mediaSessionRateLimit.userPerMinute,
  organizationLimit: config.mediaSessionRateLimit.organizationPerMinute,
  windowMs: 60_000,
  now: Date.now,
});
