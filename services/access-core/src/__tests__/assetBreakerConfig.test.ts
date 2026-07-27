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
  // Within the general 500-120000 range but below the relational floor:
  // strictly greater than the fetch deadline (3000ms) + safety margin
  // (500ms) = 3500ms, so a call admitted before the breaker opened is
  // guaranteed to have settled before resetTimeout can elapse.
  ["ACCESS_ASSET_BREAKER_RESET_MS", "3499"],
  ["ACCESS_ASSET_BREAKER_CAPACITY", "0"],
  ["ACCESS_ASSET_BREAKER_CAPACITY", "10001"],
  ["ACCESS_ASSET_BREAKER_ENABLED", "yes"],
  // Malformed numeric syntax: `Number()` would coerce every one of these to
  // a value that passes `Number.isSafeInteger`, so the format must be
  // rejected before conversion, not caught by range/safety checks alone.
  ["ACCESS_ASSET_BREAKER_CAPACITY", "5e1"],
  ["ACCESS_ASSET_BREAKER_CAPACITY", "5E1"],
  ["ACCESS_ASSET_BREAKER_CAPACITY", "0x32"],
  ["ACCESS_ASSET_BREAKER_CAPACITY", " 50"],
  ["ACCESS_ASSET_BREAKER_CAPACITY", "50 "],
  ["ACCESS_ASSET_BREAKER_CAPACITY", "\t50"],
  ["ACCESS_ASSET_BREAKER_CAPACITY", "+50"],
  ["ACCESS_ASSET_BREAKER_CAPACITY", "-50"],
  ["ACCESS_ASSET_BREAKER_CAPACITY", ""],
  ["ACCESS_ASSET_BREAKER_CAPACITY", "NaN"],
  ["ACCESS_ASSET_BREAKER_CAPACITY", "Infinity"],
  ["ACCESS_ASSET_BREAKER_CAPACITY", "-Infinity"],
  // All-digit but out of both safe-integer and configured range.
  ["ACCESS_ASSET_BREAKER_CAPACITY", "99999999999999999999"],
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

  it("defaults capacity to the provisional, unmeasured starting candidate of 50 when unset", async () => {
    vi.resetModules();

    const { config } = await import("../config");

    expect(config.assetBreaker.capacity).toBe(50);
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
    ["ACCESS_ASSET_BREAKER_RESET_MS", "3500", "resetTimeoutMs", 3500],
    ["ACCESS_ASSET_BREAKER_CAPACITY", "10000", "capacity", 10000],
    ["ACCESS_ASSET_BREAKER_CAPACITY", "0800", "capacity", 800],
  ] as const)("accepts valid %s=%s", async (name, value, key, expected) => {
    vi.stubEnv(name, value);
    vi.resetModules();

    const { config } = await import("../config");

    expect(config.assetBreaker[key]).toBe(expected);
  });
});
