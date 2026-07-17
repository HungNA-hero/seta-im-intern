import { describe, test, expect, vi, beforeEach } from "vitest";
import { GraphQLError } from "graphql";
import { createCanDoMock } from "./helpers/canDoMock";

const { mockCanDo } = vi.hoisted(() => ({
  mockCanDo: vi.fn(),
}));

const { mockFilterAllowedResourceIds } = vi.hoisted(() => ({
  mockFilterAllowedResourceIds: vi.fn(),
}));

vi.mock("../db/queries/canDo", () =>
  createCanDoMock(mockCanDo, mockFilterAllowedResourceIds),
);
vi.mock("../config", () => ({
  config: {
    goAssetUrl: "http://go-mock",
    assetInternalApiToken: "test-internal-token",
  },
}));
const { mockObjectPermissionDeleteMany } = vi.hoisted(() => ({
  mockObjectPermissionDeleteMany: vi.fn(),
}));
vi.mock("../db/prisma", () => ({
  prisma: {
    user: { findUnique: vi.fn() },
    objectPermission: { deleteMany: mockObjectPermissionDeleteMany },
  },
}));

const { mockGetFolderMeta } = vi.hoisted(() => ({
  mockGetFolderMeta: vi.fn(),
}));
vi.mock("../clients/assetClient", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../clients/assetClient")>();
  return { ...actual, getFolderMeta: mockGetFolderMeta };
});

const mockFetch = vi.fn();
vi.stubGlobal("fetch", mockFetch);

import { metadataResolvers } from "../graphql/resolvers/metadataResolvers";
import type { GraphQLContext } from "../graphql/context";

// -- fixtures -----------------------------------------------------------------

/** Builds the authenticated org context used by direct resolver tests. */
function makeCtx(overrides: Partial<GraphQLContext> = {}): GraphQLContext {
  return {
    userId: "user-1",
    currentOrgId: "org-1",
    isMember: true,
    roles: ["org_admin"],
    olpEnabled: false,
    ...overrides,
  };
}

/** Builds a complete Go metadata response so field mapping cannot pass on partial fixtures. */
function makeGoMetadataItem(overrides: Record<string, unknown> = {}) {
  return {
    id: "meta-id",
    folder_id: "folder-1",
    title: "Test Metadata",
    description: null,
    labels: ["test"],
    category: null,
    external_source: null,
    external_id: null,
    source_url: null,
    thumbnail_url: null,
    license: null,
    author: null,
    metadata_json: {},
    notes: null,
    created_by: "user-1",
    updated_by: null,
    created_at: "2024-01-01T00:00:00Z",
    updated_at: "2024-01-01T00:00:00Z",
    ...overrides,
  };
}

/** Configures one successful Go item envelope. */
function fetchOk(item: ReturnType<typeof makeGoMetadataItem>) {
  mockFetch.mockResolvedValueOnce({
    ok: true,
    status: 200,
    json: async () => ({ status: "success", item }),
  });
}

/** Configures one successful Go metadata-list envelope. */
function fetchListOk(items: ReturnType<typeof makeGoMetadataItem>[]) {
  mockFetch.mockResolvedValueOnce({
    ok: true,
    status: 200,
    json: async () => ({ status: "success", count: items.length, items }),
  });
}

/** Configures one successful internal keyset candidate page. */
function fetchCursorPageOk(
  items: ReturnType<typeof makeGoMetadataItem>[],
  hasMore: boolean,
) {
  mockFetch.mockResolvedValueOnce({
    ok: true,
    status: 200,
    json: async () => ({ status: "success", count: items.length, items, hasMore }),
  });
}

/** Configures one trusted Asset Core error envelope. */
function fetchError(status: number) {
  const errorByStatus: Record<number, [string, number]> = {
    400: ["BAD_REQUEST", 1001],
    1003: ["CURSOR_INVALID", 1003],
    401: ["UNAUTHENTICATED", 2001],
    403: ["FORBIDDEN", 2003],
    404: ["METADATA_NOT_FOUND", 4001],
    409: ["METADATA_IDENTITY_CONFLICT", 4002],
    500: ["INTERNAL_ERROR", 1000],
  };
  const [code, number] = errorByStatus[status] ?? errorByStatus[500];
  mockFetch.mockResolvedValueOnce({
    ok: false,
    status,
    json: async () => ({
      error: {
        code,
        number,
        message: "ignored test fixture text",
        traceId: "a".repeat(32),
        service: "asset-core",
      },
    }),
  });
}

beforeEach(() => {
  vi.resetAllMocks();
  mockCanDo.mockResolvedValue({ allowed: true, reason: null });
  mockFilterAllowedResourceIds.mockImplementation(
    async (_u: string, _o: string, _a: string, _r: string, ids: string[]) => new Set(ids),
  );
  mockGetFolderMeta.mockResolvedValue(null);
});

// -- Query.metadataItems -------------------------------------------------------

describe("Query.metadataItems", () => {
  const org = "org-1";
  const folder = "folder-1";
  const ctx = makeCtx();

  test("returns metadata items on success", async () => {
    fetchListOk([makeGoMetadataItem()]);

    const result = await metadataResolvers.Query.metadataItems(
      undefined,
      { orgId: org, folderId: folder },
      ctx,
    );

    expect(result).toHaveLength(1);
    expect(result[0]).toMatchObject({ id: "meta-id", title: "Test Metadata" });
  });

  test("filters items through filterAllowedResourceIds", async () => {
    fetchListOk([makeGoMetadataItem({ id: "m1" }), makeGoMetadataItem({ id: "m2" })]);
    mockFilterAllowedResourceIds.mockResolvedValueOnce(new Set(["m1"]));

    const result = await metadataResolvers.Query.metadataItems(
      undefined,
      { orgId: org, folderId: folder },
      ctx,
    );

    expect(result).toHaveLength(1);
    expect(result[0].id).toBe("m1");
  });

  test("org member with zero grants gets empty list, not FORBIDDEN", async () => {
    fetchListOk([makeGoMetadataItem({ id: "m1" })]);
    mockFilterAllowedResourceIds.mockResolvedValueOnce(new Set());

    const result = await metadataResolvers.Query.metadataItems(
      undefined,
      { orgId: org, folderId: folder },
      ctx,
    );

    expect(result).toHaveLength(0);
  });

  test("checks folder read policy once before fetching from Go", async () => {
    fetchListOk([]);
    await metadataResolvers.Query.metadataItems(
      undefined,
      { orgId: org, folderId: folder },
      ctx,
    );

    expect(mockCanDo).toHaveBeenCalledTimes(1);
    expect(mockCanDo).toHaveBeenCalledWith("user-1", "read", "folder", folder, org);
  });

  test("denies before any Go request when folder read policy denies", async () => {
    mockCanDo.mockResolvedValueOnce({ allowed: false, reason: "no RBAC ceiling" });

    await expect(
      metadataResolvers.Query.metadataItems(
        undefined,
        { orgId: org, folderId: folder },
        ctx,
      ),
    ).rejects.toMatchObject({ extensions: expect.objectContaining({ code: "FORBIDDEN" }) });

    expect(mockFetch).not.toHaveBeenCalled();
  });

  test("fetches each distinct folder's ancestry once, not once per item", async () => {
    fetchListOk([
      makeGoMetadataItem({ id: "m1", folder_id: "folder-a" }),
      makeGoMetadataItem({ id: "m2", folder_id: "folder-a" }),
      makeGoMetadataItem({ id: "m3", folder_id: "folder-b" }),
    ]);

    await metadataResolvers.Query.metadataItems(
      undefined,
      { orgId: org, folderId: folder },
      ctx,
    );

    expect(mockGetFolderMeta).toHaveBeenCalledTimes(2);
    expect(mockGetFolderMeta).toHaveBeenCalledWith(org, ctx.userId, "folder-a");
    expect(mockGetFolderMeta).toHaveBeenCalledWith(org, ctx.userId, "folder-b");
  });
});

// -- Query.metadataItem --------------------------------------------------------

describe("Query.metadataItem", () => {
  const org = "org-1";
  const id = "meta-1";
  const ctx = makeCtx();

  test("returns metadata item on success", async () => {
    fetchOk(makeGoMetadataItem({ id: "meta-1" }));

    const result = await metadataResolvers.Query.metadataItem(
      undefined,
      { orgId: org, id },
      ctx,
    );

    expect(result).toMatchObject({ id: "meta-1", title: "Test Metadata" });
  });

  test("returns null on 404 after authorized", async () => {
    mockFetch.mockResolvedValueOnce({ ok: false, status: 404 });

    const result = await metadataResolvers.Query.metadataItem(
      undefined,
      { orgId: org, id },
      ctx,
    );

    expect(result).toBeNull();
  });

  test("calls canDo with read metadata_item permission", async () => {
    fetchOk(makeGoMetadataItem());
    await metadataResolvers.Query.metadataItem(
      undefined,
      { orgId: org, id },
      ctx,
    );

    expect(mockCanDo).toHaveBeenCalledWith(
      "user-1",
      "read",
      "metadata_item",
      id,
      org,
    );
  });
});

// -- Mutation.createMetadata ---------------------------------------------------

describe("Mutation.createMetadata", () => {
  const org = "org-1";
  const ctx = makeCtx();

  test("creates metadata and returns item", async () => {
    fetchOk(makeGoMetadataItem({ title: "New Meta" }));

    const result = await metadataResolvers.Mutation.createMetadata(
      undefined,
      { orgId: org, input: { folderId: "folder-1", title: "New Meta" } },
      ctx,
    );

    expect(result).toMatchObject({ id: "meta-id", title: "New Meta" });
  });

  test("calls canDo with write folder permission", async () => {
    fetchOk(makeGoMetadataItem());
    await metadataResolvers.Mutation.createMetadata(
      undefined,
      { orgId: org, input: { folderId: "folder-1", title: "New Meta" } },
      ctx,
    );

    expect(mockCanDo).toHaveBeenCalledWith(
      "user-1",
      "write",
      "folder",
      "folder-1",
      org,
    );
  });

  test("parses metadataJson and passes object to Go", async () => {
    fetchOk(makeGoMetadataItem());
    await metadataResolvers.Mutation.createMetadata(
      undefined,
      {
        orgId: org,
        input: { folderId: "f", title: "t", metadataJson: '{"key":"value"}' },
      },
      ctx,
    );

    const body = JSON.parse(mockFetch.mock.calls[0][1].body);
    expect(body.metadata_json).toEqual({ key: "value" });
  });

  test("throws BAD_USER_INPUT if metadataJson is an array", async () => {
    await expect(
      metadataResolvers.Mutation.createMetadata(
        undefined,
        {
          orgId: org,
          input: { folderId: "f", title: "t", metadataJson: '["value"]' },
        },
        ctx,
      ),
    ).rejects.toThrow(
      expect.objectContaining({ extensions: expect.objectContaining({ code: "BAD_USER_INPUT" }) }),
    );
  });

  test("throws BAD_USER_INPUT if metadataJson is invalid JSON", async () => {
    await expect(
      metadataResolvers.Mutation.createMetadata(
        undefined,
        {
          orgId: org,
          input: { folderId: "f", title: "t", metadataJson: "invalid" },
        },
        ctx,
      ),
    ).rejects.toThrow(
      expect.objectContaining({ extensions: expect.objectContaining({ code: "BAD_USER_INPUT" }) }),
    );
  });
});

// -- Mutation.updateMetadata ---------------------------------------------------

describe("Mutation.updateMetadata", () => {
  const org = "org-1";
  const id = "meta-1";
  const ctx = makeCtx();

  test("updates metadata and returns item", async () => {
    fetchOk(makeGoMetadataItem({ title: "Updated Meta" }));

    const result = await metadataResolvers.Mutation.updateMetadata(
      undefined,
      { orgId: org, id, input: { title: "Updated Meta" } },
      ctx,
    );

    expect(result).toMatchObject({ id: "meta-id", title: "Updated Meta" });
  });

  test("throws BAD_USER_INPUT if input is empty", async () => {
    await expect(
      metadataResolvers.Mutation.updateMetadata(
        undefined,
        { orgId: org, id, input: {} },
        ctx,
      ),
    ).rejects.toThrow(
      expect.objectContaining({ extensions: expect.objectContaining({ code: "BAD_USER_INPUT" }) }),
    );
  });

  test("calls canDo with write metadata_item permission", async () => {
    fetchOk(makeGoMetadataItem());
    await metadataResolvers.Mutation.updateMetadata(
      undefined,
      { orgId: org, id, input: { title: "Updated Meta" } },
      ctx,
    );

    expect(mockCanDo).toHaveBeenCalledWith(
      "user-1",
      "write",
      "metadata_item",
      id,
      org,
    );
  });

  test("handles explicit null for metadataJson", async () => {
    fetchOk(makeGoMetadataItem());
    await metadataResolvers.Mutation.updateMetadata(
      undefined,
      { orgId: org, id, input: { metadataJson: null } },
      ctx,
    );

    const body = JSON.parse(mockFetch.mock.calls[0][1].body);
    expect(body).toHaveProperty("metadata_json", null);
  });

  test("transforms camelCase to snake_case", async () => {
    fetchOk(makeGoMetadataItem());
    await metadataResolvers.Mutation.updateMetadata(
      undefined,
      { orgId: org, id, input: { externalId: "123", sourceUrl: "http" } },
      ctx,
    );

    const body = JSON.parse(mockFetch.mock.calls[0][1].body);
    expect(body).toHaveProperty("external_id", "123");
    expect(body).toHaveProperty("source_url", "http");
    expect(body).not.toHaveProperty("externalId");
  });
});

describe("metadata transport and failure gates", () => {
  const orgId = "org-1";
  const folderId = "folder-1";
  const metadataId = "meta-1";
  const ctx = makeCtx();

  test("list forwards the exact URL and requester headers", async () => {
    fetchListOk([]);

    await metadataResolvers.Query.metadataItems(
      undefined,
      { orgId, folderId: "folder/with space" },
      ctx,
    );

    expect(mockFetch).toHaveBeenCalledWith(
      "http://go-mock/internal/api/v1/metadata-items?orgId=org-1&folderId=folder%2Fwith%20space",
      {
        method: undefined,
        headers: {
          "X-User-Id": "user-1",
          "X-Org-Id": "org-1",
          Authorization: "Bearer test-internal-token",
        },
      },
    );
  });

  test("create sends POST JSON with default metadata_json object", async () => {
    fetchOk(makeGoMetadataItem());

    await metadataResolvers.Mutation.createMetadata(
      undefined,
      { orgId, input: { folderId, title: "Title" } },
      ctx,
    );

    const [url, request] = mockFetch.mock.calls[0] as [string, RequestInit];
    expect(url).toBe(
      "http://go-mock/internal/api/v1/metadata-items?orgId=org-1",
    );
    expect(request.method).toBe("POST");
    expect(request.headers).toEqual({
      "X-User-Id": "user-1",
      "X-Org-Id": "org-1",
      Authorization: "Bearer test-internal-token",
      "Content-Type": "application/json",
    });
    expect(JSON.parse(request.body as string)).toEqual({
      folder_id: folderId,
      title: "Title",
      metadata_json: {},
    });
  });

  test("update sends PATCH and preserves omitted fields", async () => {
    fetchOk(makeGoMetadataItem());

    await metadataResolvers.Mutation.updateMetadata(
      undefined,
      { orgId, id: metadataId, input: { description: null } },
      ctx,
    );

    const [url, request] = mockFetch.mock.calls[0] as [string, RequestInit];
    expect(url).toBe(
      "http://go-mock/internal/api/v1/metadata-items?orgId=org-1&id=meta-1",
    );
    expect(request.method).toBe("PATCH");
    expect(request.headers).toEqual({
      "X-User-Id": "user-1",
      "X-Org-Id": "org-1",
      Authorization: "Bearer test-internal-token",
      "Content-Type": "application/json",
    });
    expect(JSON.parse(request.body as string)).toEqual({ description: null });
  });

  test("denied create stops before Go", async () => {
    mockCanDo.mockResolvedValueOnce({ allowed: false, reason: "denied" });

    await expect(
      metadataResolvers.Mutation.createMetadata(
        undefined,
        { orgId, input: { folderId, title: "Title" } },
        ctx,
      ),
    ).rejects.toThrow(
      expect.objectContaining({ extensions: expect.objectContaining({ code: "FORBIDDEN" }) }),
    );
    expect(mockFetch).not.toHaveBeenCalled();
  });

  test("denied update stops before Go", async () => {
    mockCanDo.mockResolvedValueOnce({ allowed: false, reason: "denied" });

    await expect(
      metadataResolvers.Mutation.updateMetadata(
        undefined,
        { orgId, id: metadataId, input: { title: "Updated" } },
        ctx,
      ),
    ).rejects.toThrow(
      expect.objectContaining({ extensions: expect.objectContaining({ code: "FORBIDDEN" }) }),
    );
    expect(mockFetch).not.toHaveBeenCalled();
  });

  test("policy exception fails closed before returning items", async () => {
    fetchListOk([makeGoMetadataItem()]);
    mockFilterAllowedResourceIds.mockRejectedValueOnce(new Error("policy unavailable"));

    await expect(
      metadataResolvers.Query.metadataItems(
        undefined,
        { orgId, folderId },
        ctx,
      ),
    ).rejects.toThrow("policy unavailable");
  });

  test("missing authentication fails before policy and Go", async () => {
    await expect(
      metadataResolvers.Query.metadataItems(
        undefined,
        { orgId, folderId },
        makeCtx({ userId: null }),
      ),
    ).rejects.toThrow(
      expect.objectContaining({ extensions: expect.objectContaining({ code: "UNAUTHENTICATED" }) }),
    );
    expect(mockCanDo).not.toHaveBeenCalled();
    expect(mockFetch).not.toHaveBeenCalled();
  });

  test.each([
    [400, "BAD_REQUEST"],
    [401, "UNAUTHENTICATED"],
    [403, "FORBIDDEN"],
    [404, "METADATA_NOT_FOUND"],
    [409, "METADATA_IDENTITY_CONFLICT"],
    [500, "INTERNAL_ERROR"],
  ])("maps trusted Asset Core status %i envelope to %s", async (status, code) => {
    fetchError(status);

    await expect(
      metadataResolvers.Query.metadataItems(
        undefined,
        { orgId, folderId },
        ctx,
      ),
    ).rejects.toThrow(expect.objectContaining({ extensions: expect.objectContaining({ code }) }));
  });

  test("rejects a malformed successful list envelope", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      status: 200,
      json: async () => ({ status: "success" }),
    });

    await expect(
      metadataResolvers.Query.metadataItems(
        undefined,
        { orgId, folderId },
        ctx,
      ),
    ).rejects.toThrow(
      expect.objectContaining({
        extensions: expect.objectContaining({ code: "INTERNAL_ERROR" }),
      }),
    );
  });

  test("rejects a malformed successful mutation envelope", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      status: 201,
      json: async () => ({ status: "success" }),
    });

    await expect(
      metadataResolvers.Mutation.createMetadata(
        undefined,
        { orgId, input: { folderId, title: "Title" } },
        ctx,
      ),
    ).rejects.toThrow(
      expect.objectContaining({
        extensions: expect.objectContaining({ code: "INTERNAL_ERROR" }),
      }),
    );
  });
});

// -- Query.searchMetadata ------------------------------------------------------

describe("Query.searchMetadata", () => {
  const org = "org-1";
  const ctx = makeCtx();

  test("returns items that pass policy check", async () => {
    fetchListOk([
      makeGoMetadataItem({ id: "meta-1" }),
      makeGoMetadataItem({ id: "meta-2" }),
    ]);
    mockFilterAllowedResourceIds.mockResolvedValueOnce(new Set(["meta-1"]));

    const result = await metadataResolvers.Query.searchMetadata(
      undefined,
      {
        orgId: org,
        input: { query: "test", limit: 10, offset: 0, folderId: "f1" },
      },
      ctx,
    );

    expect(result).toHaveLength(1);
    expect(result[0]).toMatchObject({ id: "meta-1" });
    const url = mockFetch.mock.calls[0][0];
    expect(url).toContain("limit=10");
    expect(url).toContain("offset=0");
    expect(url).toContain("folderId=f1");
    expect(url).toContain("query=test");
  });

  test("does not check folder permission before fetching", async () => {
    fetchListOk([]);
    await metadataResolvers.Query.searchMetadata(
      undefined,
      { orgId: org, input: { folderId: "f1" } },
      ctx,
    );

    expect(mockFetch).toHaveBeenCalled();
    expect(mockCanDo).not.toHaveBeenCalled();
    const calls = mockFilterAllowedResourceIds.mock.calls.filter(
      (c) => c[3] === "folder",
    );
    expect(calls).toHaveLength(0);
  });

  test("propagates the error and returns no data when the policy check fails", async () => {
    fetchListOk([
      makeGoMetadataItem({ id: "meta-1" }),
      makeGoMetadataItem({ id: "meta-2" }),
    ]);
    mockFilterAllowedResourceIds.mockRejectedValueOnce(
      new Error("policy exception"),
    );

    await expect(
      metadataResolvers.Query.searchMetadata(
        undefined,
        { orgId: org, input: { query: "test" } },
        ctx,
      ),
    ).rejects.toThrow("policy exception");
    expect(mockFilterAllowedResourceIds).toHaveBeenCalledTimes(1);
  });

  test.each([
    [{ query: " " }, "query"],
    [{ query: "a" }, "query"],
    [{ labels: ["valid", " "] }, "labels"],
    [{ limit: 0, query: "valid" }, "limit"],
    [{ limit: 101, query: "valid" }, "limit"],
    [{ offset: -1, query: "valid" }, "offset"],
    [{ offset: 10001, query: "valid" }, "offset"],
    [{}, "filter"],
  ])("rejects invalid search input before Go: %s", async (input, message) => {
    await expect(
      metadataResolvers.Query.searchMetadata(
        undefined,
        { orgId: org, input },
        ctx,
      ),
    ).rejects.toThrow(
      expect.objectContaining({ message: expect.stringContaining(message) }),
    );
    expect(mockFetch).not.toHaveBeenCalled();
    expect(mockFilterAllowedResourceIds).not.toHaveBeenCalled();
  });

  test("normalizes filters and de-duplicates labels before Go", async () => {
    fetchListOk([]);

    await metadataResolvers.Query.searchMetadata(
      undefined,
      {
        orgId: org,
        input: {
          query: "  test query  ",
          labels: [" alpha ", "alpha", "beta"],
          category: " photos ",
          externalSource: " dam ",
        },
      },
      ctx,
    );

    expect(mockFetch.mock.calls[0][0]).toBe(
      "http://go-mock/internal/api/v1/metadata-items/search?orgId=org-1&query=test%20query&label=alpha&label=beta&category=photos&externalSource=dam&limit=50&offset=0",
    );
  });

  test("passes all filter combinations including repeated labels to Go", async () => {
    fetchListOk([]);
    await metadataResolvers.Query.searchMetadata(
      undefined,
      {
        orgId: org,
        input: {
          folderId: "f1",
          query: "test",
          labels: ["a", "b"],
          category: "cat1",
          externalSource: "ext1",
          limit: 20,
          offset: 5,
        },
      },
      ctx,
    );

    const url = mockFetch.mock.calls[0][0];
    expect(url).toContain("folderId=f1");
    expect(url).toContain("query=test");
    expect(url).toContain("label=a");
    expect(url).toContain("label=b");
    expect(url).toContain("category=cat1");
    expect(url).toContain("externalSource=ext1");
    expect(url).toContain("limit=20");
    expect(url).toContain("offset=5");
  });
});

// -- Query.searchMetadataConnection ------------------------------------------

describe("Query.searchMetadataConnection", () => {
  const org = "org-1";
  const ctx = makeCtx();
  const idA = "00000000-0000-4000-8000-000000000001";
  const idB = "00000000-0000-4000-8000-000000000002";
  const idC = "00000000-0000-4000-8000-000000000003";

  test("fills a visible page across candidate batches without exposing denied cursor state", async () => {
    const timestamp = "2026-07-17T10:00:00.000000000Z";
    fetchCursorPageOk(
      [
        makeGoMetadataItem({ id: idA, updated_at: timestamp }),
        makeGoMetadataItem({ id: idB, updated_at: timestamp }),
      ],
      true,
    );
    fetchCursorPageOk([makeGoMetadataItem({ id: idC, updated_at: timestamp })], false);
    mockFilterAllowedResourceIds
      .mockResolvedValueOnce(new Set([idA]))
      .mockResolvedValueOnce(new Set([idC]));

    const result = await metadataResolvers.Query.searchMetadataConnection(
      undefined,
      { orgId: org, input: { folderId: "folder-1", first: 1 } },
      ctx,
    );

    expect(result.nodes).toEqual([expect.objectContaining({ id: idA })]);
    expect(result.pageInfo.hasNextPage).toBe(true);
    expect(result.pageInfo.endCursor).not.toBeNull();
    const cursorPayload = JSON.parse(
      Buffer.from(result.pageInfo.endCursor!, "base64url").toString("utf8"),
    );
    expect(cursorPayload.id).toBe(idA);
    expect(mockFetch.mock.calls[1][0]).toContain(`afterId=${idB}`);
    expect(mockFetch.mock.calls[1][0]).toContain("cursor=true");
  });

  test("returns CURSOR_INVALID before Asset Core for a malformed public cursor", async () => {
    await expect(
      metadataResolvers.Query.searchMetadataConnection(
        undefined,
        { orgId: org, input: { folderId: "folder-1", first: 1, after: "bad" } },
        ctx,
      ),
    ).rejects.toThrow(
      expect.objectContaining({
        extensions: expect.objectContaining({ code: "CURSOR_INVALID", number: 1003 }),
      }),
    );
    expect(mockFetch).not.toHaveBeenCalled();
  });

  test.each([0, 101])("rejects first=%i outside the public connection limit", async (first) => {
    await expect(
      metadataResolvers.Query.searchMetadataConnection(
        undefined,
        { orgId: org, input: { folderId: "folder-1", first } },
        ctx,
      ),
    ).rejects.toThrow(
      expect.objectContaining({ extensions: expect.objectContaining({ code: "BAD_USER_INPUT" }) }),
    );
    expect(mockFetch).not.toHaveBeenCalled();
  });

  test("preserves Asset Core stale cursor contract and trace boundary", async () => {
    const cursor = Buffer.from(
      JSON.stringify({ v: 1, updatedAt: "2026-07-17T10:00:00Z", id: idA }),
    ).toString("base64url");
    fetchError(1003);

    await expect(
      metadataResolvers.Query.searchMetadataConnection(
        undefined,
        { orgId: org, input: { folderId: "folder-1", after: cursor } },
        ctx,
      ),
    ).rejects.toThrow(
      expect.objectContaining({
        extensions: expect.objectContaining({ code: "CURSOR_INVALID", number: 1003 }),
      }),
    );
  });

  test("fails closed after ten sparse authorization candidate batches", async () => {
    for (let batch = 0; batch < 10; batch += 1) {
      fetchCursorPageOk(
        [
          makeGoMetadataItem({
            id: `00000000-0000-4000-8000-${String(batch + 10).padStart(12, "0")}`,
          }),
        ],
        true,
      );
    }
    mockFilterAllowedResourceIds.mockResolvedValue(new Set());

    await expect(
      metadataResolvers.Query.searchMetadataConnection(
        undefined,
        { orgId: org, input: { folderId: "folder-1", first: 1 } },
        ctx,
      ),
    ).rejects.toThrow(
      expect.objectContaining({ extensions: expect.objectContaining({ code: "INTERNAL_ERROR" }) }),
    );
    expect(mockFetch).toHaveBeenCalledTimes(10);
  });
});

// -- Mutation.deleteMetadata ---------------------------------------------------

describe("Mutation.deleteMetadata", () => {
  const org = "org-1";
  const id = "meta-1";
  const ctx = makeCtx();

  test("deletes metadata and returns true", async () => {
    mockFetch.mockResolvedValueOnce({ ok: true, status: 204 });

    const result = await metadataResolvers.Mutation.deleteMetadata(
      undefined,
      { orgId: org, id },
      ctx,
    );

    expect(result).toBe(true);
    expect(mockFetch).toHaveBeenCalledWith(
      "http://go-mock/internal/api/v1/metadata-items?orgId=org-1&id=meta-1",
      expect.objectContaining({ method: "DELETE" }),
    );
  });

  test("preserves object permission grants on the deleted item", async () => {
    mockFetch.mockResolvedValueOnce({ ok: true, status: 204 });

    await metadataResolvers.Mutation.deleteMetadata(
      undefined,
      { orgId: org, id },
      ctx,
    );

    expect(mockObjectPermissionDeleteMany).not.toHaveBeenCalled();
  });

  test("calls canDo with delete metadata_item permission", async () => {
    mockFetch.mockResolvedValueOnce({ ok: true, status: 204 });
    await metadataResolvers.Mutation.deleteMetadata(
      undefined,
      { orgId: org, id },
      ctx,
    );

    expect(mockCanDo).toHaveBeenCalledWith(
      "user-1",
      "delete",
      "metadata_item",
      id,
      org,
    );
  });

  test("denied delete stops before Go", async () => {
    mockCanDo.mockResolvedValueOnce({ allowed: false, reason: "denied" });

    await expect(
      metadataResolvers.Mutation.deleteMetadata(
        undefined,
        { orgId: org, id },
        ctx,
      ),
    ).rejects.toThrow(
      expect.objectContaining({ extensions: expect.objectContaining({ code: "FORBIDDEN" }) }),
    );
    expect(mockFetch).not.toHaveBeenCalled();
  });

  test("policy exception stops before Go", async () => {
    mockCanDo.mockRejectedValueOnce(new Error("policy error"));

    await expect(
      metadataResolvers.Mutation.deleteMetadata(
        undefined,
        { orgId: org, id },
        ctx,
      ),
    ).rejects.toThrow("policy error");
    expect(mockFetch).not.toHaveBeenCalled();
  });
});
