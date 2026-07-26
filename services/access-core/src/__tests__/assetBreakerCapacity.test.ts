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
const options = makeBreakerOptions({ capacity: 2 });

beforeEach(() => {
  global.fetch = mockFetch;
  mockFetch.mockReset();
  resetMetricsForTests();
});

afterEach(() => {
  global.fetch = originalFetch;
  vi.useRealTimers();
});

describe("asset dependency concurrency capacity", () => {
  it("rejects N+1 immediately without I/O and frees slots as calls settle", async () => {
    const harness = createAssetBreakerForTests(options);
    const first = deferredResponse();
    const second = deferredResponse();
    const third = deferredResponse();
    mockFetch
      .mockImplementationOnce(() => first.promise)
      .mockImplementationOnce(() => second.promise)
      .mockImplementationOnce(() => third.promise);

    try {
      const one = harness.fire("http://asset/one", {});
      const two = harness.fire("http://asset/two", {});
      await Promise.resolve();
      expect(harness.snapshot().inFlight).toBe(2);

      const startedAt = performance.now();
      await expect(
        harness.fire("http://asset/overflow", {}),
      ).rejects.toMatchObject({
        extensions: { code: "INTERNAL_ERROR", service: "access-core" },
      });
      expect(performance.now() - startedAt).toBeLessThan(50);
      expect(mockFetch).toHaveBeenCalledTimes(2);

      first.resolve(new Response(null, { status: 200 }));
      await one;
      expect(harness.snapshot().inFlight).toBe(1);

      const replacement = harness.fire("http://asset/replacement", {});
      await Promise.resolve();
      expect(mockFetch).toHaveBeenCalledTimes(3);
      expect(harness.snapshot().inFlight).toBe(2);

      second.resolve(new Response(null, { status: 200 }));
      third.resolve(new Response(null, { status: 200 }));
      await Promise.all([two, replacement]);
      expect(harness.snapshot().inFlight).toBe(0);
    } finally {
      harness.shutdown();
    }
  });

  it("keeps a healthy saturated burst out of breaker failure accounting", async () => {
    const harness = createAssetBreakerForTests(options);
    const slow = [deferredResponse(), deferredResponse()];
    mockFetch
      .mockImplementationOnce(() => slow[0].promise)
      .mockImplementationOnce(() => slow[1].promise);

    try {
      const admitted = [
        harness.fire("http://asset/one", {}),
        harness.fire("http://asset/two", {}),
      ];
      await Promise.resolve();
      const overflow = Array.from({ length: 20 }, (_, index) =>
        harness.fire(`http://asset/overflow-${index}`, {}),
      );

      const results = await Promise.allSettled(overflow);
      expect(results.every((result) => result.status === "rejected")).toBe(true);
      expect(mockFetch).toHaveBeenCalledTimes(options.capacity);
      expect(harness.snapshot()).toMatchObject({
        state: "closed",
        inFlight: options.capacity,
        stats: { failures: 0 },
      });
      expect(getMetricsSnapshotForTests().counters).toMatchObject({
        asset_breaker_capacity_rejected: 20,
        asset_breaker_reject: 0,
      });

      slow.forEach(({ resolve }) =>
        resolve(new Response(null, { status: 200 })),
      );
      await Promise.all(admitted);
    } finally {
      harness.shutdown();
    }
  });

  it("does not treat saturation during recovery as a successful probe", async () => {
    vi.useFakeTimers();
    const harness = createAssetBreakerForTests({ ...options, capacity: 1 });
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
        harness.fire("http://asset/saturated", {}),
      ).rejects.toMatchObject({
        extensions: { code: "INTERNAL_ERROR" },
      });
      expect(harness.snapshot().state).toBe("halfOpen");
      expect(getMetricsSnapshotForTests().counters).toMatchObject({
        asset_breaker_capacity_rejected: 1,
        asset_breaker_reject: 0,
      });

      probeResult.resolve(new Response(null, { status: 200 }));
      await probe;
    } finally {
      harness.shutdown();
    }
  });
});
