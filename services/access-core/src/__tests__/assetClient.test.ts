import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { GraphQLError } from "graphql";

vi.mock("../cache/factCache", () => ({
  readFolderFactThrough: (
    _orgId: string,
    _id: string,
    loader: () => Promise<unknown>,
  ) => loader(),
  readItemFactThrough: (
    _orgId: string,
    _id: string,
    loader: () => Promise<unknown>,
  ) => loader(),
}));

import {
  assetPath,
  assetFetch,
  unwrapEnvelope,
  unwrapListEnvelope,
  unwrap204,
  snakeCaseKeys,
  throwGoError,
  getFolderMeta,
  getMetadataMeta,
} from "../clients/assetClient";
import { config } from "../config";
import {
  createRequestCorrelation,
  runWithRequestCorrelation,
} from "../observability/requestContext";

describe("assetClient", () => {
  const mockFetch = vi.fn();
  const originalFetch = global.fetch;

  beforeEach(() => {
    global.fetch = mockFetch;
    mockFetch.mockReset();
  });

  afterEach(() => {
    global.fetch = originalFetch;
  });

  describe("assetPath", () => {
    it("should format path with basic parameters", () => {
      expect(assetPath("/test", { id: "123", active: true })).toBe(
        "/test?id=123&active=true",
      );
    });

    it("should omit undefined parameters", () => {
      expect(
        assetPath("/test", { id: "123", skip: undefined, active: true }),
      ).toBe("/test?id=123&active=true");
    });

    it("should format repeated array parameters", () => {
      expect(assetPath("/test", { id: "123", label: ["a", "b c"] })).toBe(
        "/test?id=123&label=a&label=b%20c",
      );
    });
  });

  describe("snakeCaseKeys", () => {
    it("should convert camelCase to snake_case", () => {
      expect(snakeCaseKeys({ fooBar: 1, baz: 2, quxQuuxQuuz: 3 })).toEqual({
        foo_bar: 1,
        baz: 2,
        qux_quux_quuz: 3,
      });
    });
  });

  describe("throwGoError", () => {
    it("preserves the trusted Asset Core error contract", async () => {
      const response = new Response(
        JSON.stringify({
          error: {
            code: "METADATA_NOT_FOUND",
            number: 4001,
            message: "untrusted response text",
            traceId: "a".repeat(32),
            service: "asset-core",
          },
        }),
        { status: 404 },
      );

      await expect(throwGoError(response)).rejects.toMatchObject({
        message: "Metadata item not found",
        extensions: {
          code: "METADATA_NOT_FOUND",
          number: 4001,
          traceId: "a".repeat(32),
          service: "asset-core",
        },
      });
    });

    it("fails closed when the dependency response is malformed", async () => {
      await expect(
        throwGoError(new Response("gateway diagnostic", { status: 502 })),
      ).rejects.toMatchObject({
        extensions: { code: "INTERNAL_ERROR", number: 1000 },
      });
    });
  });

  describe("assetFetch", () => {
    it("should attach headers and serialize body", async () => {
      mockFetch.mockResolvedValue(new Response());
      await assetFetch("/test", {
        userId: "u1",
        orgId: "o1",
        method: "POST",
        body: { foo: "bar" },
      });

      expect(mockFetch).toHaveBeenCalledWith(`${config.goAssetUrl}/test`, {
        method: "POST",
        headers: {
          "X-User-Id": "u1",
          "X-Org-Id": "o1",
          Authorization: `Bearer ${config.assetInternalApiToken}`,
          "Content-Type": "application/json",
        },
        body: '{"foo":"bar"}',
        signal: expect.any(AbortSignal),
      });
    });

    it("should support DELETE method", async () => {
      mockFetch.mockResolvedValue(new Response());
      await assetFetch("/test", {
        userId: "u1",
        orgId: "o1",
        method: "DELETE",
      });

      expect(mockFetch).toHaveBeenCalledWith(`${config.goAssetUrl}/test`, {
        method: "DELETE",
        headers: {
          "X-User-Id": "u1",
          "X-Org-Id": "o1",
          Authorization: `Bearer ${config.assetInternalApiToken}`,
        },
        signal: expect.any(AbortSignal),
      });
    });

    it("retries a failed GET request once", async () => {
      mockFetch
        .mockResolvedValueOnce(new Response(null, { status: 503 }))
        .mockResolvedValueOnce(new Response(null, { status: 200 }));

      const response = await assetFetch("/test", { userId: "u1", orgId: "o1" });

      expect(response.status).toBe(200);
      expect(mockFetch).toHaveBeenCalledTimes(2);
    });

    it("does not retry a failed mutation without an idempotency contract", async () => {
      mockFetch.mockResolvedValue(new Response(null, { status: 503 }));

      const response = await assetFetch("/test", {
        userId: "u1",
        orgId: "o1",
        method: "POST",
        body: { name: "once-only" },
      });

      expect(response.status).toBe(503);
      expect(mockFetch).toHaveBeenCalledTimes(1);
    });

    it("fails closed when the dependency trace id is not canonical", async () => {
      await expect(
        throwGoError(
          new Response(
            JSON.stringify({
              error: {
                code: "FOLDER_NOT_FOUND",
                number: 3001,
                traceId: "not-a-trace-id",
                service: "asset-core",
              },
            }),
            { status: 404 },
          ),
        ),
      ).rejects.toMatchObject({
        extensions: { code: "INTERNAL_ERROR", service: "access-core" },
      });
    });

    it("forwards traceparent and request id only inside a request context", async () => {
      mockFetch.mockResolvedValue(new Response());
      const correlation = createRequestCorrelation({
        traceparent: `00-${"b".repeat(32)}-${"c".repeat(16)}-01`,
        "x-request-id": "request-57",
      });

      await runWithRequestCorrelation(correlation, () =>
        assetFetch("/test", { userId: "u1", orgId: "o1" }),
      );

      expect(mockFetch).toHaveBeenCalledWith(`${config.goAssetUrl}/test`, {
        method: undefined,
        headers: {
          "X-User-Id": "u1",
          "X-Org-Id": "o1",
          Authorization: `Bearer ${config.assetInternalApiToken}`,
          traceparent: correlation.traceparent,
          "x-request-id": "request-57",
        },
        signal: expect.any(AbortSignal),
      });
    });
  });

  describe("unwrap functions", () => {
    it("should unwrap envelope successfully", async () => {
      mockFetch.mockResolvedValue(
        new Response(JSON.stringify({ data: { id: 1 } }), { status: 200 }),
      );
      const res = await mockFetch();
      const val = await unwrapEnvelope(res, "data", (x) => x, "Err");
      expect(val).toEqual({ id: 1 });
    });

    it("should throw malformed envelope if key missing", async () => {
      mockFetch.mockResolvedValue(
        new Response(JSON.stringify({ wrong: 1 }), { status: 200 }),
      );
      const res = await mockFetch();
      await expect(
        unwrapEnvelope(res, "data", (x) => x, "Err"),
      ).rejects.toThrow("Err: unexpected response format");
    });

    it("should unwrap list envelope successfully", async () => {
      mockFetch.mockResolvedValue(
        new Response(JSON.stringify({ list: [{ id: 1 }] }), { status: 200 }),
      );
      const res = await mockFetch();
      const val = await unwrapListEnvelope(res, "list", (x) => x, "Err");
      expect(val).toEqual([{ id: 1 }]);
    });

    it("should throw malformed list envelope if not array", async () => {
      mockFetch.mockResolvedValue(
        new Response(JSON.stringify({ list: { id: 1 } }), { status: 200 }),
      );
      const res = await mockFetch();
      await expect(
        unwrapListEnvelope(res, "list", (x) => x, "Err"),
      ).rejects.toThrow("Err: unexpected response format");
    });

    it("should handle 204 successfully", async () => {
      mockFetch.mockResolvedValue(new Response(null, { status: 204 }));
      const res = await mockFetch();
      const val = await unwrap204(res, "Err");
      expect(val).toBe(true);
    });

    it("should throw if not 204", async () => {
      mockFetch.mockResolvedValue(new Response(null, { status: 400 }));
      const res = await mockFetch();
      await expect(unwrap204(res, "Err")).rejects.toThrow();
    });
  });

  describe("getFolderMeta", () => {
    it("returns null on 404 (folder does not exist)", async () => {
      mockFetch.mockResolvedValue(new Response(null, { status: 404 }));
      const result = await getFolderMeta("org-1", "user-1", "f1");
      expect(result).toBeNull();
    });

    it("returns the folder path on success", async () => {
      mockFetch.mockResolvedValue(
        new Response(JSON.stringify({ folder: { path: "abc.def" } }), {
          status: 200,
        }),
      );
      const result = await getFolderMeta("org-1", "user-1", "f1");
      expect(result).toEqual({ path: "abc.def" });
    });

    it("fails closed for a malformed 500 response instead of resolving to null", async () => {
      mockFetch.mockResolvedValue(
        new Response(null, { status: 500, statusText: "Internal Error" }),
      );
      await expect(getFolderMeta("org-1", "user-1", "f1")).rejects.toMatchObject(
        { extensions: { code: "INTERNAL_ERROR", number: 1000 } },
      );
    });

    it("fails closed for a malformed 403 response instead of resolving to null", async () => {
      mockFetch.mockResolvedValue(
        new Response(null, { status: 403, statusText: "Forbidden" }),
      );
      await expect(getFolderMeta("org-1", "user-1", "f1")).rejects.toMatchObject(
        { extensions: { code: "INTERNAL_ERROR", number: 1000 } },
      );
    });
  });

  describe("getMetadataMeta", () => {
    it("returns null on 404 (item does not exist)", async () => {
      mockFetch.mockResolvedValue(new Response(null, { status: 404 }));
      const result = await getMetadataMeta("org-1", "user-1", "m1");
      expect(result).toBeNull();
    });

    it("returns the containing folder id on success", async () => {
      mockFetch.mockResolvedValue(
        new Response(JSON.stringify({ item: { folder_id: "f1" } }), {
          status: 200,
        }),
      );
      const result = await getMetadataMeta("org-1", "user-1", "m1");
      expect(result).toEqual({ folderId: "f1" });
    });

    it("fails closed for a malformed 500 response instead of resolving to null", async () => {
      mockFetch.mockResolvedValue(
        new Response(null, { status: 500, statusText: "Internal Error" }),
      );
      await expect(
        getMetadataMeta("org-1", "user-1", "m1"),
      ).rejects.toMatchObject({
        extensions: { code: "INTERNAL_ERROR", number: 1000 },
      });
    });
  });
});
