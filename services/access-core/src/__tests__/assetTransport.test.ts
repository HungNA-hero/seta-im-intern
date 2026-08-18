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

  test("sends correlated media PUT JSON without accepting a binary body", async () => {
    const executor = { fire: vi.fn().mockResolvedValue(new Response(null, { status: 202 })) };
    const transport = createAssetTransport({
      baseUrl: "http://asset-core",
      internalApiToken: "token",
      executor,
      getCorrelation: () => ({
        traceparent: "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01",
        requestId: "request-media-1",
      }),
    });

    await transport.request("/internal/api/v1/metadata-items/asset-1/media", {
      userId: "user-1",
      orgId: "org-1",
      method: "PUT",
      body: { upload_id: "upload-1" },
    });

    expect(executor.fire).toHaveBeenCalledWith(
      "http://asset-core/internal/api/v1/metadata-items/asset-1/media",
      expect.objectContaining({
        method: "PUT",
        body: JSON.stringify({ upload_id: "upload-1" }),
        headers: expect.objectContaining({
          "Content-Type": "application/json",
          traceparent: "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01",
          "x-request-id": "request-media-1",
        }),
      }),
    );
  });
});
