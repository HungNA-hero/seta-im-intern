import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createAssetBreakerForTests } from "../clients/assetBreaker";
import {
  getMetricsSnapshotForTests,
  resetMetricsForTests,
} from "../cache/metrics";
import {
  deferredResponse,
  makeBreakerOptions,
  openBreaker,
} from "./helpers/assetBreakerTestFixtures";

const originalFetch = global.fetch;
const mockFetch = vi.fn();
const options = makeBreakerOptions();

beforeEach(() => {
  vi.useFakeTimers();
  global.fetch = mockFetch;
  mockFetch.mockReset();
  resetMetricsForTests();
});

afterEach(() => {
  global.fetch = originalFetch;
  vi.useRealTimers();
});

describe("asset dependency breaker recovery", () => {
  it("admits exactly one probe and closes automatically when it succeeds", async () => {
    const harness = createAssetBreakerForTests(options);
    try {
      await openBreaker(harness, mockFetch);
      expect(harness.snapshot().state).toBe("open");
      await vi.advanceTimersByTimeAsync(options.resetTimeoutMs);
      expect(harness.snapshot().state).toBe("halfOpen");

      const probeResult = deferredResponse();
      mockFetch.mockImplementationOnce(() => probeResult.promise);
      const probe = harness.fire("http://asset/probe", {});
      await Promise.resolve();

      await expect(
        harness.fire("http://asset/concurrent", {}),
      ).rejects.toMatchObject({
        extensions: { code: "INTERNAL_ERROR", service: "access-core" },
      });
      expect(mockFetch).toHaveBeenCalledTimes(3);

      probeResult.resolve(new Response(null, { status: 200 }));
      await expect(probe).resolves.toMatchObject({ status: 200 });
      expect(harness.snapshot().state).toBe("closed");
      expect(getMetricsSnapshotForTests().counters).toMatchObject({
        asset_breaker_half_open: 1,
        asset_breaker_close: 1,
      });
    } finally {
      harness.shutdown();
    }
  });

  it("reopens after one failed probe and surfaces its original response", async () => {
    const harness = createAssetBreakerForTests(options);
    try {
      await openBreaker(harness, mockFetch);
      expect(harness.snapshot().state).toBe("open");
      await vi.advanceTimersByTimeAsync(options.resetTimeoutMs);
      mockFetch.mockClear();
      mockFetch.mockResolvedValueOnce(new Response(null, { status: 503 }));

      await expect(
        harness.fire("http://asset/probe", {}),
      ).resolves.toMatchObject({ status: 503 });
      expect(mockFetch).toHaveBeenCalledTimes(1);
      expect(harness.snapshot().state).toBe("open");
    } finally {
      harness.shutdown();
    }
  });

  it("never adds a second caller-side request after a failing probe", async () => {
    const harness = createAssetBreakerForTests(options);
    try {
      await openBreaker(harness, mockFetch);
      expect(harness.snapshot().state).toBe("open");
      await vi.advanceTimersByTimeAsync(options.resetTimeoutMs);
      mockFetch.mockClear();
      mockFetch.mockResolvedValueOnce(new Response(null, { status: 500 }));

      await harness.fire("http://asset/probe", {});
      expect(mockFetch).toHaveBeenCalledTimes(1);
      expect(harness.snapshot().state).toBe("open");
    } finally {
      harness.shutdown();
    }
  });
});
