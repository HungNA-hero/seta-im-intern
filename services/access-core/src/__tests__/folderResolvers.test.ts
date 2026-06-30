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

import { folderResolvers } from "../graphql/resolvers/folderResolvers";
import type { GraphQLContext } from "../graphql/context";

// ── fixtures ─────────────────────────────────────────────────────────────────

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

function makeGoFolder(overrides: Record<string, unknown> = {}) {
  return {
    id: "folder-id",
    org_id: "org-1",
    path: "abc123",
    name: "My Folder",
    description: null,
    created_by: "user-1",
    updated_by: null,
    created_at: "2024-01-01T00:00:00Z",
    updated_at: "2024-01-01T00:00:00Z",
    ...overrides,
  };
}

function fetchOk(folder: ReturnType<typeof makeGoFolder>) {
  mockFetch.mockResolvedValueOnce({
    ok: true,
    json: async () => ({ status: "success", folder }),
  });
}

function fetchError(status: number, statusText = "Error") {
  mockFetch.mockResolvedValueOnce({ ok: false, status, statusText });
}

beforeEach(() => {
  vi.resetAllMocks();
  mockCanDo.mockResolvedValue({ allowed: true, reason: null });
});

// ── Mutation.createFolder ─────────────────────────────────────────────────────

describe("Mutation.createFolder", () => {
  const org = "org-1";
  const ctx = makeCtx();

  test("returns folder on success", async () => {
    const raw = makeGoFolder({ name: "Photos" });
    fetchOk(raw);

    const result = await folderResolvers.Mutation.createFolder(
      undefined,
      { orgId: org, name: "Photos" },
      ctx,
    );

    expect(result).toMatchObject({ id: raw.id, name: "Photos", orgId: org });
  });

  test("posts to Go with correct URL and headers", async () => {
    fetchOk(makeGoFolder());
    await folderResolvers.Mutation.createFolder(
      undefined,
      { orgId: org, name: "Test" },
      ctx,
    );

    const [url, init] = mockFetch.mock.calls[0];
    expect(url).toBe(`http://go-mock/internal/api/v1/folders?orgId=${org}`);
    expect(init.method).toBe("POST");
    expect(init.headers["X-User-Id"]).toBe("user-1");
    expect(init.headers["X-Org-Id"]).toBe(org);
    expect(init.headers["Content-Type"]).toBe("application/json");
  });

  test("includes parent_path in body when parentPath is provided", async () => {
    fetchOk(makeGoFolder());
    await folderResolvers.Mutation.createFolder(
      undefined,
      { orgId: org, name: "Child", parentPath: "abc123" },
      ctx,
    );

    const body = JSON.parse(mockFetch.mock.calls[0][1].body);
    expect(body).toEqual({ name: "Child", parent_path: "abc123" });
  });

  test("omits parent_path from body when parentPath is undefined", async () => {
    fetchOk(makeGoFolder());
    await folderResolvers.Mutation.createFolder(
      undefined,
      { orgId: org, name: "Root" },
      ctx,
    );

    const body = JSON.parse(mockFetch.mock.calls[0][1].body);
    expect(body).not.toHaveProperty("parent_path");
  });

  test("includes description in body when provided", async () => {
    fetchOk(makeGoFolder({ description: "A desc" }));
    await folderResolvers.Mutation.createFolder(
      undefined,
      { orgId: org, name: "Root", description: "A desc" },
      ctx,
    );

    const body = JSON.parse(mockFetch.mock.calls[0][1].body);
    expect(body.description).toBe("A desc");
  });

  test("omits description from body when undefined", async () => {
    fetchOk(makeGoFolder());
    await folderResolvers.Mutation.createFolder(
      undefined,
      { orgId: org, name: "Root" },
      ctx,
    );

    const body = JSON.parse(mockFetch.mock.calls[0][1].body);
    expect(body).not.toHaveProperty("description");
  });

  test("uses org-scoped permission check (resourceId === orgId)", async () => {
    fetchOk(makeGoFolder());
    await folderResolvers.Mutation.createFolder(
      undefined,
      { orgId: org, name: "Test" },
      ctx,
    );

    expect(mockCanDo).toHaveBeenCalledWith("user-1", "write", "folder", org, org);
  });

  test("throws FORBIDDEN when canDo denies", async () => {
    mockCanDo.mockResolvedValueOnce({ allowed: false, reason: "no RBAC ceiling" });

    await expect(
      folderResolvers.Mutation.createFolder(
        undefined,
        { orgId: org, name: "Test" },
        ctx,
      ),
    ).rejects.toThrow(
      expect.objectContaining({ extensions: { code: "FORBIDDEN" } }),
    );
    expect(mockFetch).not.toHaveBeenCalled();
  });

  test("throws UNAUTHENTICATED when userId is null", async () => {
    await expect(
      folderResolvers.Mutation.createFolder(
        undefined,
        { orgId: org, name: "Test" },
        makeCtx({ userId: null }),
      ),
    ).rejects.toThrow(
      expect.objectContaining({ extensions: { code: "UNAUTHENTICATED" } }),
    );
    expect(mockCanDo).not.toHaveBeenCalled();
  });

  test("throws CONFLICT when Go returns 409", async () => {
    fetchError(409, "Conflict");

    await expect(
      folderResolvers.Mutation.createFolder(
        undefined,
        { orgId: org, name: "Dupe" },
        ctx,
      ),
    ).rejects.toThrow(
      expect.objectContaining({ extensions: { code: "CONFLICT" } }),
    );
  });

  test("throws BAD_USER_INPUT when Go returns 400", async () => {
    fetchError(400, "Bad Request");

    await expect(
      folderResolvers.Mutation.createFolder(
        undefined,
        { orgId: org, name: "  " },
        ctx,
      ),
    ).rejects.toThrow(
      expect.objectContaining({ extensions: { code: "BAD_USER_INPUT" } }),
    );
  });

  test("throws INTERNAL_SERVER_ERROR when response has no folder", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ status: "success" }),
    });

    await expect(
      folderResolvers.Mutation.createFolder(
        undefined,
        { orgId: org, name: "Test" },
        ctx,
      ),
    ).rejects.toThrow(
      expect.objectContaining({ extensions: { code: "INTERNAL_SERVER_ERROR" } }),
    );
  });
});

// ── Mutation.updateFolder ─────────────────────────────────────────────────────

describe("Mutation.updateFolder", () => {
  const org = "org-1";
  const folderId = "folder-id";
  const ctx = makeCtx();

  test("returns updated folder on success", async () => {
    const raw = makeGoFolder({ name: "Renamed" });
    fetchOk(raw);

    const result = await folderResolvers.Mutation.updateFolder(
      undefined,
      { orgId: org, id: folderId, name: "Renamed" },
      ctx,
    );

    expect(result).toMatchObject({ id: folderId, name: "Renamed" });
  });

  test("patches Go with correct URL and headers", async () => {
    fetchOk(makeGoFolder());
    await folderResolvers.Mutation.updateFolder(
      undefined,
      { orgId: org, id: folderId, name: "New" },
      ctx,
    );

    const [url, init] = mockFetch.mock.calls[0];
    expect(url).toBe(`http://go-mock/internal/api/v1/folders?orgId=${org}&id=${folderId}`);
    expect(init.method).toBe("PATCH");
    expect(init.headers["X-User-Id"]).toBe("user-1");
    expect(init.headers["X-Org-Id"]).toBe(org);
  });

  test("uses resource-scoped permission check (resourceId === folderId)", async () => {
    fetchOk(makeGoFolder());
    await folderResolvers.Mutation.updateFolder(
      undefined,
      { orgId: org, id: folderId, name: "New" },
      ctx,
    );

    expect(mockCanDo).toHaveBeenCalledWith("user-1", "write", "folder", folderId, org);
  });

  test("forwards null description to Go (clears it)", async () => {
    fetchOk(makeGoFolder({ description: null }));
    await folderResolvers.Mutation.updateFolder(
      undefined,
      { orgId: org, id: folderId, description: null },
      ctx,
    );

    const body = JSON.parse(mockFetch.mock.calls[0][1].body);
    expect(body).toHaveProperty("description", null);
  });

  test("omits name from body when not provided", async () => {
    fetchOk(makeGoFolder());
    await folderResolvers.Mutation.updateFolder(
      undefined,
      { orgId: org, id: folderId, description: "New desc" },
      ctx,
    );

    const body = JSON.parse(mockFetch.mock.calls[0][1].body);
    expect(body).not.toHaveProperty("name");
    expect(body.description).toBe("New desc");
  });

  test("throws BAD_USER_INPUT when neither name nor description is provided — before auth check", async () => {
    await expect(
      folderResolvers.Mutation.updateFolder(
        undefined,
        { orgId: org, id: folderId },
        ctx,
      ),
    ).rejects.toThrow(
      expect.objectContaining({ extensions: { code: "BAD_USER_INPUT" } }),
    );
    expect(mockCanDo).not.toHaveBeenCalled();
  });

  test("throws BAD_USER_INPUT when name is null — before auth check", async () => {
    await expect(
      folderResolvers.Mutation.updateFolder(
        undefined,
        { orgId: org, id: folderId, name: null },
        ctx,
      ),
    ).rejects.toThrow(
      expect.objectContaining({ extensions: { code: "BAD_USER_INPUT" } }),
    );
    expect(mockCanDo).not.toHaveBeenCalled();
  });

  test("throws FORBIDDEN when canDo denies", async () => {
    mockCanDo.mockResolvedValueOnce({ allowed: false, reason: "no object permission" });

    await expect(
      folderResolvers.Mutation.updateFolder(
        undefined,
        { orgId: org, id: folderId, name: "New" },
        ctx,
      ),
    ).rejects.toThrow(
      expect.objectContaining({ extensions: { code: "FORBIDDEN" } }),
    );
    expect(mockFetch).not.toHaveBeenCalled();
  });

  test("throws UNAUTHENTICATED when userId is null", async () => {
    await expect(
      folderResolvers.Mutation.updateFolder(
        undefined,
        { orgId: org, id: folderId, name: "New" },
        makeCtx({ userId: null }),
      ),
    ).rejects.toThrow(
      expect.objectContaining({ extensions: { code: "UNAUTHENTICATED" } }),
    );
  });

  test("throws CONFLICT when Go returns 409", async () => {
    fetchError(409, "Conflict");

    await expect(
      folderResolvers.Mutation.updateFolder(
        undefined,
        { orgId: org, id: folderId, name: "Dupe" },
        ctx,
      ),
    ).rejects.toThrow(
      expect.objectContaining({ extensions: { code: "CONFLICT" } }),
    );
  });

  test("throws NOT_FOUND when Go returns 404", async () => {
    fetchError(404, "Not Found");

    await expect(
      folderResolvers.Mutation.updateFolder(
        undefined,
        { orgId: org, id: "nonexistent", name: "X" },
        ctx,
      ),
    ).rejects.toThrow(
      expect.objectContaining({ extensions: { code: "NOT_FOUND" } }),
    );
  });
});

// ── Query.folder ──────────────────────────────────────────────────────────────

describe("Query.folder", () => {
  const org = "org-1";
  const folderId = "folder-id";
  const ctx = makeCtx();

  test("returns folder when found", async () => {
    const raw = makeGoFolder();
    mockFetch.mockResolvedValueOnce({
      ok: true,
      status: 200,
      json: async () => ({ folder: raw }),
    });

    const result = await folderResolvers.Query.folder(
      undefined,
      { orgId: org, id: folderId },
      ctx,
    );

    expect(result).toMatchObject({ id: raw.id, name: raw.name, orgId: org });
  });

  test("returns null on 404", async () => {
    mockFetch.mockResolvedValueOnce({ ok: false, status: 404 });

    const result = await folderResolvers.Query.folder(
      undefined,
      { orgId: org, id: "missing" },
      ctx,
    );

    expect(result).toBeNull();
  });

  test("passes orgId in URL and header", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      status: 200,
      json: async () => ({ folder: makeGoFolder() }),
    });

    await folderResolvers.Query.folder(
      undefined,
      { orgId: org, id: folderId },
      ctx,
    );

    const [url, init] = mockFetch.mock.calls[0];
    expect(url).toContain(`orgId=${org}`);
    expect(url).toContain(`id=${folderId}`);
    expect(init.headers["X-Org-Id"]).toBe(org);
  });

  test("uses resource-scoped read permission check", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      status: 200,
      json: async () => ({ folder: makeGoFolder() }),
    });

    await folderResolvers.Query.folder(
      undefined,
      { orgId: org, id: folderId },
      ctx,
    );

    expect(mockCanDo).toHaveBeenCalledWith("user-1", "read", "folder", folderId, org);
  });

  test("throws FORBIDDEN when canDo denies", async () => {
    mockCanDo.mockResolvedValueOnce({ allowed: false, reason: "denied" });

    await expect(
      folderResolvers.Query.folder(undefined, { orgId: org, id: folderId }, ctx),
    ).rejects.toThrow(
      expect.objectContaining({ extensions: { code: "FORBIDDEN" } }),
    );
    expect(mockFetch).not.toHaveBeenCalled();
  });
});
