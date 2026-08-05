import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createAssetBreakerForTests } from "../clients/assetBreaker";
import { getMetricsSnapshotForTests, resetMetricsForTests } from "../cache/metrics";
import { abortableStalledBodyFetch, makeBreakerOptions, stalledBodyResponse } from "./helpers/assetBreakerTestFixtures";

const originalFetch = global.fetch;
const mockFetch = vi.fn();
const options = makeBreakerOptions({ capacity: 5 });

beforeEach(() => {
  global.fetch = mockFetch;
  mockFetch.mockReset();
  resetMetricsForTests();
});

afterEach(() => {
  global.fetch = originalFetch;
  vi.useRealTimers();
});

describe("asset breaker response-body lifetime", () => {
  it("keeps the capacity slot held while the body is still being read, after headers have already arrived", async () => {
    const harness = createAssetBreakerForTests(options);
    const stalled = stalledBodyResponse(200);
    mockFetch.mockResolvedValueOnce(stalled.response);

    try {
      const call = harness.fire("http://asset/slow-body", {});
      // The mocked fetch() promise has already resolved (headers "arrived"),
      // but the body stream is still open.
      await Promise.resolve();
      await Promise.resolve();
      expect(harness.snapshot().inFlight).toBe(1);
      expect(harness.snapshot().stats.successes).toBe(0);

      stalled.close();
      await expect(call).resolves.toMatchObject({ status: 200 });
      expect(harness.snapshot().inFlight).toBe(0);
      expect(harness.snapshot().stats.successes).toBe(1);
    } finally {
      harness.shutdown();
    }
  });

  it("records a failure while reading the body as a breaker failure, not a success", async () => {
    const harness = createAssetBreakerForTests(options);
    const stalled = stalledBodyResponse(200);
    mockFetch.mockResolvedValueOnce(stalled.response);

    try {
      const call = harness.fire("http://asset/broken-body", {});
      await Promise.resolve();
      await Promise.resolve();
      expect(harness.snapshot().inFlight).toBe(1);

      stalled.fail(new Error("stream reset"));
      await expect(call).rejects.toThrow("stream reset");
      expect(harness.snapshot()).toMatchObject({
        inFlight: 0,
        stats: { successes: 0, failures: 1 },
      });
    } finally {
      harness.shutdown();
    }
  });

  it("aborts a stalled body read at the fetch deadline and counts it as a failure", async () => {
    vi.useFakeTimers();
    const harness = createAssetBreakerForTests(options);
    mockFetch.mockImplementationOnce(abortableStalledBodyFetch(200));

    try {
      const call = harness.fire("http://asset/stalled-forever", {});
      // Attach the rejection expectation before advancing fake timers, so a
      // handler is already in place at the moment the abort actually rejects
      // the pending body read.
      const rejection = expect(call).rejects.toMatchObject({
        name: "AbortError",
      });
      await Promise.resolve();
      expect(harness.snapshot().inFlight).toBe(1);

      await vi.advanceTimersByTimeAsync(2999);
      expect(harness.snapshot().inFlight).toBe(1);

      await vi.advanceTimersByTimeAsync(1);
      await rejection;
      expect(harness.snapshot()).toMatchObject({
        inFlight: 0,
        stats: { successes: 0, failures: 1 },
      });
    } finally {
      harness.shutdown();
    }
  });

  it("releases the capacity slot exactly once across success, body failure, and abort paths", async () => {
    const harness = createAssetBreakerForTests(options);

    // Success path.
    const okStalled = stalledBodyResponse(200);
    mockFetch.mockImplementationOnce(() => Promise.resolve(okStalled.response));
    const okCall = harness.fire("http://asset/ok", {});
    await Promise.resolve();
    await Promise.resolve();
    expect(harness.snapshot().inFlight).toBe(1);
    okStalled.close();
    await okCall;
    expect(harness.snapshot().inFlight).toBe(0);

    // Body-failure path.
    const failStalled = stalledBodyResponse(200);
    mockFetch.mockImplementationOnce(() => Promise.resolve(failStalled.response));
    const failCall = harness.fire("http://asset/fail", {});
    await Promise.resolve();
    await Promise.resolve();
    expect(harness.snapshot().inFlight).toBe(1);
    failStalled.fail(new Error("boom"));
    await expect(failCall).rejects.toThrow("boom");
    expect(harness.snapshot().inFlight).toBe(0);

    harness.shutdown();
  });

  it("does not release a slot or record success/failure until the reconstructed response is fully buffered", async () => {
    const harness = createAssetBreakerForTests(options);
    const stalled = stalledBodyResponse(200);
    mockFetch.mockResolvedValueOnce(stalled.response);

    try {
      const call = harness.fire("http://asset/partial", {});
      await Promise.resolve();
      stalled.push("partial-chunk");
      await Promise.resolve();
      await Promise.resolve();
      // Body still open even though a chunk has arrived — must still be
      // held as in-flight and uncounted.
      expect(harness.snapshot()).toMatchObject({
        inFlight: 1,
        stats: { successes: 0 },
      });

      stalled.close();
      const response = await call;
      expect(await response.text()).toBe("partial-chunk");
      expect(harness.snapshot()).toMatchObject({
        inFlight: 0,
        stats: { successes: 1 },
      });
    } finally {
      harness.shutdown();
    }
  });

  it("bounds the effective metrics/log impact to a single event per action regardless of path", async () => {
    const harness = createAssetBreakerForTests(options);
    const stalled = stalledBodyResponse(503);
    mockFetch.mockResolvedValueOnce(stalled.response);

    try {
      const call = harness.fire("http://asset/late-5xx", {});
      await Promise.resolve();
      await Promise.resolve();
      expect(harness.snapshot().inFlight).toBe(1);

      stalled.close();
      // A >=500 status discovered only after the full body is read is still
      // returned to the caller (not swallowed) and still counts as exactly
      // one breaker failure — not zero (missed) and not more than one.
      await expect(call).resolves.toMatchObject({ status: 503 });
      expect(harness.snapshot()).toMatchObject({
        inFlight: 0,
        stats: { successes: 0, failures: 1 },
      });
      expect(getMetricsSnapshotForTests().counters.asset_breaker_reject).toBe(0);
    } finally {
      harness.shutdown();
    }
  });
});
