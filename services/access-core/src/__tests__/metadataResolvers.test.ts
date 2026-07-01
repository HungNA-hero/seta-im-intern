import { describe, test, expect, vi, beforeEach } from "vitest";
import { GraphQLError } from "graphql";

const { mockCanDo } = vi.hoisted(() => ({
  mockCanDo: vi.fn(),
}));

vi.mock("../db/queries/canDo", () => ({ canDo: mockCanDo }));
vi.mock("../config", () => ({ config: { goAssetUrl: "http://go-mock" } }));
vi.mock("../db/prisma", () => ({ prisma: { user: { findUnique: vi.fn() } } }));

const mockFetch = vi.fn();
vi.stubGlobal("fetch", mockFetch);

import { metadataResolvers } from "../graphql/resolvers/metadataResolvers";
import type { GraphQLContext } from "../graphql/context";

// ── fixtures ─────────────────────────────────────────────────────────────────

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

/** Configures one failed Go response for status-to-GraphQL error mapping tests. */
function fetchError(status: number, statusText = "Error") {
  mockFetch.mockResolvedValueOnce({ ok: false, status, statusText });
}

beforeEach(() => {
  vi.resetAllMocks();
  mockCanDo.mockResolvedValue({ allowed: true, reason: null });
});

// ── Query.metadataItems ───────────────────────────────────────────────────────

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

  test("calls canDo with read folder permission", async () => {
    fetchListOk([]);
    await metadataResolvers.Query.metadataItems(
      undefined,
      { orgId: org, folderId: folder },
      ctx,
    );

    expect(mockCanDo).toHaveBeenCalledWith("user-1", "read", "folder", folder, org);
  });

  test("throws FORBIDDEN when canDo denies", async () => {
    mockCanDo.mockResolvedValueOnce({ allowed: false, reason: "denied" });

    await expect(
      metadataResolvers.Query.metadataItems(undefined, { orgId: org, folderId: folder }, ctx),
    ).rejects.toThrow(expect.objectContaining({ extensions: { code: "FORBIDDEN" } }));
    expect(mockFetch).not.toHaveBeenCalled();
  });
});

// ── Query.metadataItem ────────────────────────────────────────────────────────

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

    expect(mockCanDo).toHaveBeenCalledWith("user-1", "read", "metadata_item", id, org);
  });
});

// ── Mutation.createMetadata ───────────────────────────────────────────────────

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

    expect(mockCanDo).toHaveBeenCalledWith("user-1", "write", "folder", "folder-1", org);
  });

  test("parses metadataJson and passes object to Go", async () => {
    fetchOk(makeGoMetadataItem());
    await metadataResolvers.Mutation.createMetadata(
      undefined,
      { orgId: org, input: { folderId: "f", title: "t", metadataJson: '{"key":"value"}' } },
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
      expect.objectContaining({ extensions: { code: "BAD_USER_INPUT" } }),
    );
  });

  test("throws BAD_USER_INPUT if metadataJson is invalid JSON", async () => {
    await expect(
      metadataResolvers.Mutation.createMetadata(
        undefined,
        { orgId: org, input: { folderId: "f", title: "t", metadataJson: 'invalid' } },
        ctx,
      ),
    ).rejects.toThrow(
      expect.objectContaining({ extensions: { code: "BAD_USER_INPUT" } }),
    );
  });
});

// ── Mutation.updateMetadata ───────────────────────────────────────────────────

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
      metadataResolvers.Mutation.updateMetadata(undefined, { orgId: org, id, input: {} }, ctx)
    ).rejects.toThrow(expect.objectContaining({ extensions: { code: "BAD_USER_INPUT" } }));
  });

  test("calls canDo with write metadata_item permission", async () => {
    fetchOk(makeGoMetadataItem());
    await metadataResolvers.Mutation.updateMetadata(
      undefined,
      { orgId: org, id, input: { title: "Updated Meta" } },
      ctx,
    );

    expect(mockCanDo).toHaveBeenCalledWith("user-1", "write", "metadata_item", id, org);
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
      { headers: { "X-User-Id": "user-1", "X-Org-Id": "org-1" } },
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
    expect(url).toBe("http://go-mock/internal/api/v1/metadata-items?orgId=org-1");
    expect(request.method).toBe("POST");
    expect(request.headers).toEqual({
      "X-User-Id": "user-1",
      "X-Org-Id": "org-1",
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
      expect.objectContaining({ extensions: { code: "FORBIDDEN" } }),
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
      expect.objectContaining({ extensions: { code: "FORBIDDEN" } }),
    );
    expect(mockFetch).not.toHaveBeenCalled();
  });

  test("policy exception fails closed before Go", async () => {
    mockCanDo.mockRejectedValueOnce(new Error("policy unavailable"));

    await expect(
      metadataResolvers.Query.metadataItems(
        undefined,
        { orgId, folderId },
        ctx,
      ),
    ).rejects.toThrow("policy unavailable");
    expect(mockFetch).not.toHaveBeenCalled();
  });

  test("missing authentication fails before policy and Go", async () => {
    await expect(
      metadataResolvers.Query.metadataItems(
        undefined,
        { orgId, folderId },
        makeCtx({ userId: null }),
      ),
    ).rejects.toThrow(
      expect.objectContaining({ extensions: { code: "UNAUTHENTICATED" } }),
    );
    expect(mockCanDo).not.toHaveBeenCalled();
    expect(mockFetch).not.toHaveBeenCalled();
  });

  test.each([
    [400, "BAD_USER_INPUT"],
    [401, "UNAUTHENTICATED"],
    [403, "FORBIDDEN"],
    [404, "NOT_FOUND"],
    [409, "CONFLICT"],
    [500, "INTERNAL_SERVER_ERROR"],
  ])("maps Go status %i to %s", async (status, code) => {
    fetchError(status);

    await expect(
      metadataResolvers.Query.metadataItems(
        undefined,
        { orgId, folderId },
        ctx,
      ),
    ).rejects.toThrow(expect.objectContaining({ extensions: { code } }));
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
        extensions: { code: "INTERNAL_SERVER_ERROR" },
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
        extensions: { code: "INTERNAL_SERVER_ERROR" },
      }),
    );
  });
});
