import { describe, expect, test, vi } from "vitest";
import { AssetBreakerDependencies, createAssetBreaker } from "../clients/assetBreaker";

describe("createAssetBreaker dependency injection", () => {
  test("uses the injected fetch and timer ports", async () => {
    const dependencies: AssetBreakerDependencies = {
      fetch: vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ ok: true }), {
          status: 200,
          headers: { "content-type": "application/json" },
        }),
      ),
      recordMetric: vi.fn(),
      log: vi.fn(),
      createDependencyError: () => new Error("dependency unavailable"),
      setTimer: (callback, delayMs) => setTimeout(callback, delayMs),
      clearTimer: (timer) => clearTimeout(timer),
    };
    const breaker = createAssetBreaker(
      {
        enabled: false,
        requestTimeoutMs: 3000,
        errorThresholdPercentage: 50,
        volumeThreshold: 1,
        resetTimeoutMs: 10,
        capacity: 1,
      },
      dependencies,
    );

    const response = await breaker.fire("http://asset-core/health", {});

    expect(response.status).toBe(200);
    expect(dependencies.fetch).toHaveBeenCalledWith(
      "http://asset-core/health",
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );
    breaker.shutdown();
  });
});
