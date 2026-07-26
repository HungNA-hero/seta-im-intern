import { afterEach, describe, expect, it, vi } from "vitest";

const INVALID_CASES = [
  ["ACCESS_ASSET_BREAKER_ERROR_THRESHOLD_PCT", "abc"],
  ["ACCESS_ASSET_BREAKER_ERROR_THRESHOLD_PCT", "50junk"],
  ["ACCESS_ASSET_BREAKER_ERROR_THRESHOLD_PCT", "1.5"],
  ["ACCESS_ASSET_BREAKER_ERROR_THRESHOLD_PCT", "0"],
  ["ACCESS_ASSET_BREAKER_ERROR_THRESHOLD_PCT", "100"],
  ["ACCESS_ASSET_BREAKER_VOLUME_THRESHOLD", "0"],
  ["ACCESS_ASSET_BREAKER_VOLUME_THRESHOLD", "1001"],
  ["ACCESS_ASSET_BREAKER_RESET_MS", "499"],
  ["ACCESS_ASSET_BREAKER_RESET_MS", "120001"],
  ["ACCESS_ASSET_BREAKER_CAPACITY", "0"],
  ["ACCESS_ASSET_BREAKER_CAPACITY", "10001"],
  ["ACCESS_ASSET_BREAKER_ENABLED", "yes"],
] as const;

afterEach(() => {
  vi.unstubAllEnvs();
  vi.resetModules();
});

describe("asset breaker configuration", () => {
  it.each(INVALID_CASES)("rejects %s=%s during module evaluation", async (name, value) => {
    vi.stubEnv(name, value);
    vi.resetModules();

    await expect(import("../config")).rejects.toThrow(name);
  });

  it("parses false case-insensitively", async () => {
    vi.stubEnv("ACCESS_ASSET_BREAKER_ENABLED", "FALSE");
    vi.resetModules();

    const { config } = await import("../config");

    expect(config.assetBreaker.enabled).toBe(false);
  });

  it.each([
    ["ACCESS_ASSET_BREAKER_ERROR_THRESHOLD_PCT", "99", "errorThresholdPercentage", 99],
    ["ACCESS_ASSET_BREAKER_VOLUME_THRESHOLD", "1000", "volumeThreshold", 1000],
    ["ACCESS_ASSET_BREAKER_RESET_MS", "120000", "resetTimeoutMs", 120000],
    ["ACCESS_ASSET_BREAKER_CAPACITY", "10000", "capacity", 10000],
  ] as const)("accepts valid %s=%s", async (name, value, key, expected) => {
    vi.stubEnv(name, value);
    vi.resetModules();

    const { config } = await import("../config");

    expect(config.assetBreaker[key]).toBe(expected);
  });
});
