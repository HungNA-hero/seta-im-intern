import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { GraphQLError } from "graphql";
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
    it("should map Go error codes to GraphQL codes", () => {
      try {
        throwGoError({ status: 404, statusText: "Not Found" }, "Test");
        expect.fail("Should throw");
      } catch (e) {
        expect(e).toBeInstanceOf(GraphQLError);
        expect((e as GraphQLError).message).toBe("Test: Not Found");
        expect((e as GraphQLError).extensions.code).toBe("NOT_FOUND");
      }
    });

    it("should default to INTERNAL_SERVER_ERROR for unknown codes", () => {
      try {
        throwGoError({ status: 502, statusText: "Bad Gateway" }, "Test");
        expect.fail("Should throw");
      } catch (e) {
        expect((e as GraphQLError).extensions.code).toBe(
          "INTERNAL_SERVER_ERROR",
        );
      }
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

    it("propagates a 500 as INTERNAL_SERVER_ERROR instead of resolving to null", async () => {
      mockFetch.mockResolvedValue(
        new Response(null, { status: 500, statusText: "Internal Error" }),
      );
      await expect(getFolderMeta("org-1", "user-1", "f1")).rejects.toMatchObject(
        { extensions: { code: "INTERNAL_SERVER_ERROR" } },
      );
    });

    it("propagates a 403 as FORBIDDEN instead of resolving to null", async () => {
      mockFetch.mockResolvedValue(
        new Response(null, { status: 403, statusText: "Forbidden" }),
      );
      await expect(getFolderMeta("org-1", "user-1", "f1")).rejects.toMatchObject(
        { extensions: { code: "FORBIDDEN" } },
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

    it("propagates a 500 as INTERNAL_SERVER_ERROR instead of resolving to null", async () => {
      mockFetch.mockResolvedValue(
        new Response(null, { status: 500, statusText: "Internal Error" }),
      );
      await expect(
        getMetadataMeta("org-1", "user-1", "m1"),
      ).rejects.toMatchObject({ extensions: { code: "INTERNAL_SERVER_ERROR" } });
    });
  });
});
