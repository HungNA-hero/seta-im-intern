import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { afterEach, describe, expect, test, vi } from "vitest";

// config.ts loads the repo-root .env on every import, which would put back any
// key this file deletes. These tests assert what the code does with a value
// present or absent, so they must not see a developer's local .env at all.
vi.mock("dotenv", () => ({ config: () => ({ parsed: {} }) }));

const mediaEnvironmentKeys = [
  "ACCESS_MEDIA_UPLOAD_ENABLED",
  "ACCESS_MEDIA_SESSION_LIMIT_PER_USER_PER_MINUTE",
  "ACCESS_MEDIA_SESSION_LIMIT_PER_ORG_PER_MINUTE",
  "ACCESS_MEDIA_BREAKER_ENABLED",
  "ACCESS_MEDIA_BREAKER_ERROR_THRESHOLD_PCT",
  "ACCESS_MEDIA_BREAKER_VOLUME_THRESHOLD",
  "ACCESS_MEDIA_BREAKER_RESET_MS",
  "ACCESS_MEDIA_BREAKER_CAPACITY",
] as const;

async function loadConfig() {
  vi.resetModules();
  return (await import("../config")).config;
}

afterEach(() => {
  vi.unstubAllEnvs();
  for (const key of mediaEnvironmentKeys) delete process.env[key];
  vi.resetModules();
});

describe("media rollout configuration", () => {
  test("keeps media routes disabled when an existing deployment omits the rollout flag", async () => {
    for (const key of mediaEnvironmentKeys) delete process.env[key];

    const config = await loadConfig();

    expect(config.mediaUploadEnabled).toBe(false);
  });

  test("reads configured per-user and per-organization session limits", async () => {
    vi.stubEnv("ACCESS_MEDIA_SESSION_LIMIT_PER_USER_PER_MINUTE", "7");
    vi.stubEnv("ACCESS_MEDIA_SESSION_LIMIT_PER_ORG_PER_MINUTE", "41");

    const config = await loadConfig();

    expect(config.mediaSessionRateLimit).toEqual({ userPerMinute: 7, organizationPerMinute: 41 });
  });

  test("forwards rollout and rate-limit settings into the Access container", () => {
    const compose = readFileSync(resolve(process.cwd(), "../../infra/docker-compose.yml"), "utf8");

    expect(compose).toContain("ACCESS_MEDIA_UPLOAD_ENABLED: ${ACCESS_MEDIA_UPLOAD_ENABLED:-false}");
    expect(compose).toContain(
      "ACCESS_MEDIA_SESSION_LIMIT_PER_USER_PER_MINUTE: ${ACCESS_MEDIA_SESSION_LIMIT_PER_USER_PER_MINUTE:-10}",
    );
    expect(compose).toContain(
      "ACCESS_MEDIA_SESSION_LIMIT_PER_ORG_PER_MINUTE: ${ACCESS_MEDIA_SESSION_LIMIT_PER_ORG_PER_MINUTE:-60}",
    );
  });

  test("configures the media dependency breaker independently", async () => {
    vi.stubEnv("ACCESS_MEDIA_BREAKER_ENABLED", "false");
    vi.stubEnv("ACCESS_MEDIA_BREAKER_ERROR_THRESHOLD_PCT", "42");
    vi.stubEnv("ACCESS_MEDIA_BREAKER_VOLUME_THRESHOLD", "7");
    vi.stubEnv("ACCESS_MEDIA_BREAKER_RESET_MS", "7000");
    vi.stubEnv("ACCESS_MEDIA_BREAKER_CAPACITY", "9");

    const config = await loadConfig();

    expect(config.mediaBreaker).toEqual({
      enabled: false,
      errorThresholdPercentage: 42,
      volumeThreshold: 7,
      resetTimeoutMs: 7000,
      capacity: 9,
    });
  });

  test("forwards independent media breaker settings into the Access container", () => {
    const compose = readFileSync(resolve(process.cwd(), "../../infra/docker-compose.yml"), "utf8");
    for (const line of [
      "ACCESS_MEDIA_BREAKER_ENABLED: ${ACCESS_MEDIA_BREAKER_ENABLED-true}",
      "ACCESS_MEDIA_BREAKER_ERROR_THRESHOLD_PCT: ${ACCESS_MEDIA_BREAKER_ERROR_THRESHOLD_PCT-50}",
      "ACCESS_MEDIA_BREAKER_VOLUME_THRESHOLD: ${ACCESS_MEDIA_BREAKER_VOLUME_THRESHOLD-10}",
      "ACCESS_MEDIA_BREAKER_RESET_MS: ${ACCESS_MEDIA_BREAKER_RESET_MS-5000}",
      "ACCESS_MEDIA_BREAKER_CAPACITY: ${ACCESS_MEDIA_BREAKER_CAPACITY-50}",
    ]) {
      expect(compose).toContain(line);
    }
  });
});
