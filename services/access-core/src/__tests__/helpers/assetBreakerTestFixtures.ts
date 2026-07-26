import type { vi } from "vitest";
import type {
  AssetBreakerHarness,
  AssetBreakerOptions,
} from "../../clients/assetBreaker";

export function deferredResponse(): {
  promise: Promise<Response>;
  resolve: (response: Response) => void;
} {
  let resolve!: (response: Response) => void;
  const promise = new Promise<Response>((settle) => {
    resolve = settle;
  });
  return { promise, resolve };
}

export function makeBreakerOptions(
  overrides: Partial<AssetBreakerOptions> = {},
): AssetBreakerOptions {
  return {
    enabled: true,
    errorThresholdPercentage: 50,
    volumeThreshold: 2,
    resetTimeoutMs: 500,
    capacity: 10,
    ...overrides,
  };
}

export async function openBreaker(
  harness: AssetBreakerHarness,
  mockFetch: ReturnType<typeof vi.fn>,
): Promise<void> {
  mockFetch.mockResolvedValue(new Response(null, { status: 503 }));
  await harness.fire("http://asset/failure-a", {});
  await harness.fire("http://asset/failure-b", {});
}
