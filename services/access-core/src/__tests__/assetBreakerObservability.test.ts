import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createAssetBreakerForTests } from "../clients/assetBreaker";
import {
  deferredResponse,
  makeBreakerOptions,
  openBreaker,
} from "./helpers/assetBreakerTestFixtures";

const originalFetch = global.fetch;
const mockFetch = vi.fn();
const options = makeBreakerOptions({ capacity: 1 });

function parsedLines(
  stderrSpy: ReturnType<typeof vi.spyOn>,
): Array<Record<string, unknown>> {
  return stderrSpy.mock.calls.map(([line]: [unknown]) =>
    JSON.parse(String(line)),
  ) as Array<Record<string, unknown>>;
}

beforeEach(() => {
  vi.useFakeTimers();
  global.fetch = mockFetch;
  mockFetch.mockReset();
});

afterEach(() => {
  global.fetch = originalFetch;
  vi.unstubAllEnvs();
  vi.restoreAllMocks();
  vi.useRealTimers();
});

describe("asset breaker observability", () => {
  it("emits one contract-shaped line for every state transition", async () => {
    const stderrSpy = vi
      .spyOn(process.stderr, "write")
      .mockImplementation(() => true);
    const harness = createAssetBreakerForTests(options);
    try {
      await openBreaker(harness, mockFetch);
      await vi.advanceTimersByTimeAsync(options.resetTimeoutMs);
      mockFetch.mockResolvedValueOnce(new Response(null, { status: 200 }));
      await harness.fire("http://asset/probe", {});

      const lines = parsedLines(stderrSpy);
      expect(lines).toHaveLength(3);
      expect(lines.map(({ event }) => event)).toEqual([
        "asset_breaker_open",
        "asset_breaker_half_open",
        "asset_breaker_close",
      ]);
      expect(lines.map(({ level }) => level)).toEqual([
        "error",
        "warn",
        "warn",
      ]);
      for (const line of lines) {
        expect(line).toMatchObject({ service: "access-core" });
        expect(line.timestamp).toEqual(expect.any(String));
        expect(Number.isNaN(Date.parse(String(line.timestamp)))).toBe(false);
      }
    } finally {
      harness.shutdown();
    }
  });

  it("aggregates every rejection in a window into one summary", async () => {
    const stderrSpy = vi
      .spyOn(process.stderr, "write")
      .mockImplementation(() => true);
    const harness = createAssetBreakerForTests(options);
    const slow = deferredResponse();
    mockFetch.mockImplementationOnce(() => slow.promise);

    try {
      const admitted = harness.fire("http://asset/slow", {});
      await Promise.resolve();
      const rejected = Array.from({ length: 25 }, (_, index) =>
        harness.fire(`http://asset/rejected-${index}`, {}),
      );
      await Promise.allSettled(rejected);

      await vi.advanceTimersByTimeAsync(4999);
      expect(stderrSpy).not.toHaveBeenCalled();
      await vi.advanceTimersByTimeAsync(1);
      expect(parsedLines(stderrSpy)).toEqual([
        expect.objectContaining({
          level: "warn",
          service: "access-core",
          event: "asset_breaker_capacity_rejected_summary",
          rejectedCount: 25,
          windowMs: 5000,
          timestamp: expect.any(String),
        }),
      ]);

      await expect(
        harness.fire("http://asset/next-window", {}),
      ).rejects.toBeDefined();
      await vi.advanceTimersByTimeAsync(5000);
      expect(parsedLines(stderrSpy)).toHaveLength(2);
      expect(parsedLines(stderrSpy)[1]).toMatchObject({ rejectedCount: 1 });

      slow.resolve(new Response(null, { status: 200 }));
      await admitted;
    } finally {
      harness.shutdown();
    }
  });

  it("cancels an unflushed summary when shutdown begins", async () => {
    const stderrSpy = vi
      .spyOn(process.stderr, "write")
      .mockImplementation(() => true);
    const harness = createAssetBreakerForTests(options);
    const slow = deferredResponse();
    mockFetch.mockImplementationOnce(() => slow.promise);

    const admitted = harness.fire("http://asset/slow", {});
    await Promise.resolve();
    await expect(
      harness.fire("http://asset/rejected", {}),
    ).rejects.toBeDefined();
    harness.shutdown();
    await vi.advanceTimersByTimeAsync(5000);
    expect(stderrSpy).not.toHaveBeenCalled();

    slow.resolve(new Response(null, { status: 200 }));
    await admitted;
  });

  it("bypasses both breaker and capacity when disabled by environment", async () => {
    vi.stubEnv("ACCESS_ASSET_BREAKER_ENABLED", "false");
    vi.resetModules();
    const stderrSpy = vi
      .spyOn(process.stderr, "write")
      .mockImplementation(() => true);
    mockFetch.mockResolvedValue(new Response(null, { status: 503 }));
    const {
      fireAssetRequest,
      shutdownAssetBreaker,
    } = await import("../clients/assetBreaker");

    try {
      for (let index = 0; index < 20; index += 1) {
        await fireAssetRequest(`http://asset/failure-${index}`, {});
      }
      expect(mockFetch).toHaveBeenCalledTimes(20);
      expect(stderrSpy).not.toHaveBeenCalled();
    } finally {
      shutdownAssetBreaker();
    }
  });
});
