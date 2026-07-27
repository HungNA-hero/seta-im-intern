import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createAssetBreakerForTests } from "../clients/assetBreaker";
import {
  getMetricsSnapshotForTests,
  resetMetricsForTests,
} from "../cache/metrics";
import {
  abortableStalledBodyFetch,
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

  it("guarantees a call admitted before the breaker opened has already settled before resetTimeout can elapse, so it cannot later close half-open", async () => {
    // resetTimeoutMs matches config.ts's validated floor: strictly greater
    // than the fetch deadline (3000ms) plus its safety margin (500ms).
    const resetTimeoutMs = 3600;
    const harness = createAssetBreakerForTests(
      makeBreakerOptions({ volumeThreshold: 2, resetTimeoutMs }),
    );
    try {
      // Admitted while closed. Left unresolved by the mock itself — it only
      // ever settles when the real fetch deadline aborts it, exactly like a
      // genuinely slow (but not literally infinite) dependency call.
      mockFetch.mockImplementationOnce(abortableStalledBodyFetch(200));
      const preOpenCall = harness.fire("http://asset/slow-legit", {});
      let preOpenSettled = false;
      preOpenCall.catch(() => {}).finally(() => {
        preOpenSettled = true;
      });
      await Promise.resolve();

      // Two concurrent failures open the breaker while the pre-open call is
      // still in flight.
      mockFetch.mockResolvedValue(new Response(null, { status: 503 }));
      await harness.fire("http://asset/failure-a", {});
      await harness.fire("http://asset/failure-b", {});
      expect(harness.snapshot().state).toBe("open");
      expect(preOpenSettled).toBe(false);

      // Just before the pre-open call's own fetch deadline: still pending.
      await vi.advanceTimersByTimeAsync(2999);
      expect(preOpenSettled).toBe(false);
      expect(harness.snapshot().state).toBe("open");

      // The fetch deadline forces it to settle (aborted) — strictly before
      // resetTimeout(3600ms) has any chance to fire.
      await vi.advanceTimersByTimeAsync(1);
      expect(preOpenSettled).toBe(true);
      expect(harness.snapshot().state).toBe("open");

      // Now resetTimeout elapses: there is no pre-open call left in flight
      // that could spuriously resolve and close this half-open window.
      await vi.advanceTimersByTimeAsync(resetTimeoutMs - 3000);
      expect(harness.snapshot().state).toBe("halfOpen");
    } finally {
      harness.shutdown();
    }
  });
});
