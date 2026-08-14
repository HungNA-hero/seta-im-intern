import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { getMetricsSnapshotForTests, resetMetricsForTests } from "../cache/metrics";
import { createMediaAssetBreakerForTests, getMediaAssetBreakerSnapshot } from "../clients/assetBreaker";
import { makeBreakerOptions, openBreaker } from "./helpers/assetBreakerTestFixtures";

const originalFetch = global.fetch;
const mockFetch = vi.fn();

beforeEach(() => {
  vi.useFakeTimers();
  resetMetricsForTests();
  global.fetch = mockFetch;
  mockFetch.mockReset();
});

afterEach(() => {
  global.fetch = originalFetch;
  vi.restoreAllMocks();
  vi.useRealTimers();
});

describe("media breaker isolation", () => {
  it("labels media transitions without mutating authorization breaker counters", async () => {
    const stderr = vi.spyOn(process.stderr, "write").mockImplementation(() => true);
    const options = makeBreakerOptions();
    const breaker = createMediaAssetBreakerForTests(options);
    try {
      await openBreaker(breaker, mockFetch);
      await vi.advanceTimersByTimeAsync(options.resetTimeoutMs);
      mockFetch.mockResolvedValueOnce(new Response(null, { status: 200 }));
      await breaker.fire("http://asset/media-probe", {});

      expect(stderr.mock.calls.map(([line]) => JSON.parse(String(line)).event)).toEqual([
        "media_asset_breaker_open",
        "media_asset_breaker_half_open",
        "media_asset_breaker_close",
      ]);
      expect(getMetricsSnapshotForTests().counters).toMatchObject({
        media_asset_breaker_open: 1,
        media_asset_breaker_half_open: 1,
        media_asset_breaker_close: 1,
        asset_breaker_open: 0,
        asset_breaker_half_open: 0,
        asset_breaker_close: 0,
      });
    } finally {
      breaker.shutdown();
    }
  });

  it("exposes the production media breaker state separately", () => {
    expect(getMediaAssetBreakerSnapshot()).toMatchObject({ state: expect.stringMatching(/^(closed|open|halfOpen)$/) });
  });
});
