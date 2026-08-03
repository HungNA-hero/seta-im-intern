import type { vi } from "vitest";
import type { AssetBreakerHarness, AssetBreakerOptions } from "../../clients/assetBreaker";

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

export function makeBreakerOptions(overrides: Partial<AssetBreakerOptions> = {}): AssetBreakerOptions {
  return {
    enabled: true,
    errorThresholdPercentage: 50,
    volumeThreshold: 2,
    resetTimeoutMs: 500,
    capacity: 10,
    ...overrides,
  };
}

/**
 * A Response whose body never closes until `push`/`fail` is called — for
 * simulating a body that stalls or fails to read, independent of whether
 * headers have already "arrived" (the mocked fetch promise resolves
 * immediately with this Response; only reading the body blocks).
 */
export function stalledBodyResponse(status = 200): {
  response: Response;
  push: (chunk: string) => void;
  close: () => void;
  fail: (error: Error) => void;
} {
  let streamController!: ReadableStreamDefaultController<Uint8Array>;
  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      streamController = controller;
    },
  });
  return {
    response: new Response(stream, { status }),
    push: (chunk: string) => streamController.enqueue(new TextEncoder().encode(chunk)),
    close: () => streamController.close(),
    fail: (error: Error) => streamController.error(error),
  };
}

/**
 * Wires a stalled-body response's stream to error when `init.signal` aborts,
 * matching how a real fetch implementation propagates an abort into an
 * in-progress body read. Returns the mock implementation to hand to
 * `mockFetch.mockImplementationOnce(...)`.
 */
export function abortableStalledBodyFetch(status = 200): (url: string, init: RequestInit) => Promise<Response> {
  return (_url: string, init: RequestInit) => {
    const stalled = stalledBodyResponse(status);
    init.signal?.addEventListener("abort", () => {
      stalled.fail(new DOMException("This operation was aborted", "AbortError"));
    });
    return Promise.resolve(stalled.response);
  };
}

export async function openBreaker(harness: AssetBreakerHarness, mockFetch: ReturnType<typeof vi.fn>): Promise<void> {
  mockFetch.mockResolvedValue(new Response(null, { status: 503 }));
  await harness.fire("http://asset/failure-a", {});
  await harness.fire("http://asset/failure-b", {});
}
