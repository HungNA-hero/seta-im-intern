import { describe, expect, test, vi } from "vitest";
import { createAssetTransport } from "../clients/assetTransport";

describe("createAssetTransport", () => {
  test("builds an authenticated correlated request through the executor port", async () => {
    const response = new Response(null, { status: 204 });
    const executor = { fire: vi.fn().mockResolvedValue(response) };
    const transport = createAssetTransport({
      baseUrl: "http://asset-core",
      internalApiToken: "token",
      executor,
      getCorrelation: () => ({
        traceparent: "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01",
        requestId: "request-1",
      }),
    });

    await expect(
      transport.request("/folders", {
        userId: "user-1",
        orgId: "org-1",
        orgAdmin: true,
        method: "POST",
        body: { name: "Folder" },
      }),
    ).resolves.toBe(response);

    expect(executor.fire).toHaveBeenCalledWith(
      "http://asset-core/folders",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ name: "Folder" }),
        headers: expect.objectContaining({
          Authorization: "Bearer token",
          "X-User-Id": "user-1",
          "X-Org-Id": "org-1",
          "X-Org-Admin": "true",
          "x-request-id": "request-1",
        }),
      }),
    );
  });
});
