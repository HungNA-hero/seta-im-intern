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

// ── Query.folderTree ─────────────────────────────────────────────────────────

function fetchListOk(goFolders: ReturnType<typeof makeGoFolder>[]) {
  mockFetch.mockResolvedValueOnce({
    ok: true,
    json: async () => ({ folders: goFolders }),
  });
}

describe("Query.folderTree", () => {
  const org = "org-1";
  const ctx = makeCtx();

  test("returns mapped folders", async () => {
    const raw = [
      makeGoFolder({ id: "f1", path: "root", name: "Root" }),
      makeGoFolder({ id: "f2", path: "root.child", name: "Child" }),
    ];
    fetchListOk(raw);

    const result = await folderResolvers.Query.folderTree(undefined, { orgId: org }, ctx);

    expect(result).toHaveLength(2);
    expect(result[0]).toMatchObject({ id: "f1", name: "Root", path: "root" });
    expect(result[1]).toMatchObject({ id: "f2", name: "Child", path: "root.child" });
  });

  test("attaches subtreeNodes (same reference) to every folder when rootPath is given", async () => {
    fetchListOk([
      makeGoFolder({ id: "f1", path: "root" }),
      makeGoFolder({ id: "f2", path: "root.child" }),
    ]);

    const result = (await folderResolvers.Query.folderTree(undefined, { orgId: org, rootPath: "root" }, ctx)) as any[];

    expect(result[0].subtreeNodes).toHaveLength(2);
    expect(result[0].subtreeNodes).toBe(result[1].subtreeNodes);
  });

  test("loads and attaches the full forest cache without rootPath", async () => {
    fetchListOk([makeGoFolder({ id: "f1", path: "root" })]);

    const result = (await folderResolvers.Query.folderTree(undefined, { orgId: org }, ctx)) as any[];

    expect(result[0].subtreeNodes).toBe(result);
    expect(mockFetch.mock.calls[0][0]).toContain("tree=true");
  });

  test("makes exactly one HTTP call to Go", async () => {
    fetchListOk([makeGoFolder()]);
    await folderResolvers.Query.folderTree(undefined, { orgId: org }, ctx);
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  test("uses org-scoped permission check (resourceId === orgId)", async () => {
    fetchListOk([]);
    await folderResolvers.Query.folderTree(undefined, { orgId: org }, ctx);
    expect(mockCanDo).toHaveBeenCalledWith("user-1", "read", "folder", org, org);
  });

  test("appends rootPath to URL when provided", async () => {
    fetchListOk([]);
    await folderResolvers.Query.folderTree(undefined, { orgId: org, rootPath: "docs" }, ctx);
    const [url] = mockFetch.mock.calls[0];
    expect(url).toContain("rootPath=docs");
  });

  test("maps Go error codes (403 → FORBIDDEN, not INTERNAL_SERVER_ERROR)", async () => {
    mockFetch.mockResolvedValueOnce({ ok: false, status: 403, statusText: "Forbidden" });
    await expect(
      folderResolvers.Query.folderTree(undefined, { orgId: org }, ctx),
    ).rejects.toThrow(expect.objectContaining({ extensions: { code: "FORBIDDEN" } }));
  });

  test("throws UNAUTHENTICATED when userId is null", async () => {
    await expect(
      folderResolvers.Query.folderTree(undefined, { orgId: org }, makeCtx({ userId: null })),
    ).rejects.toThrow(expect.objectContaining({ extensions: { code: "UNAUTHENTICATED" } }));
    expect(mockFetch).not.toHaveBeenCalled();
  });
});

// ── Query.folderChildren ──────────────────────────────────────────────────────

describe("Query.folderChildren", () => {
  const org = "org-1";
  const ctx = makeCtx();

  test("returns children from Go", async () => {
    const raw = [makeGoFolder({ id: "c1", path: "root.child1" })];
    fetchListOk(raw);

    const result = await folderResolvers.Query.folderChildren(
      undefined,
      { orgId: org, parentPath: "root" },
      ctx,
    );

    expect(result).toHaveLength(1);
    expect(result[0]).toMatchObject({ id: "c1", path: "root.child1" });
  });

  test("sends children=true and parentPath in URL", async () => {
    fetchListOk([]);
    await folderResolvers.Query.folderChildren(
      undefined,
      { orgId: org, parentPath: "root" },
      ctx,
    );
    const [url] = mockFetch.mock.calls[0];
    expect(url).toContain("children=true");
    expect(url).toContain("rootPath=root");
  });

  test("uses org-scoped permission check", async () => {
    fetchListOk([]);
    await folderResolvers.Query.folderChildren(
      undefined,
      { orgId: org, parentPath: "root" },
      ctx,
    );
    expect(mockCanDo).toHaveBeenCalledWith("user-1", "read", "folder", org, org);
  });

  test("maps Go error codes (403 → FORBIDDEN)", async () => {
    mockFetch.mockResolvedValueOnce({ ok: false, status: 403, statusText: "Forbidden" });
    await expect(
      folderResolvers.Query.folderChildren(undefined, { orgId: org, parentPath: "root" }, ctx),
    ).rejects.toThrow(expect.objectContaining({ extensions: { code: "FORBIDDEN" } }));
  });
});

// ── Folder.children ───────────────────────────────────────────────────────────

describe("Folder.children", () => {
  const ctx = makeCtx();

  function node(id: string, path: string) {
    return {
      id,
      orgId: "org-1",
      path,
      name: id,
      description: null as string | null,
      createdBy: "user-1",
      updatedBy: null as string | null,
      createdAt: "2024-01-01T00:00:00Z",
      updatedAt: "2024-01-01T00:00:00Z",
    };
  }

  const allNodes = [
    node("root", "root"),
    node("child1", "root.child1"),
    node("child2", "root.child2"),
    node("grandchild", "root.child1.grandchild"),
  ];

  test("resolves direct children from cache without any HTTP call", async () => {
    const parent = { ...allNodes[0], subtreeNodes: allNodes };

    const result = await folderResolvers.Folder.children(parent, undefined, ctx);

    expect(mockFetch).not.toHaveBeenCalled();
    expect(result).toHaveLength(2);
    expect((result as any[]).map(r => r.id)).toEqual(expect.arrayContaining(["child1", "child2"]));
  });

  test("excludes grandchildren when resolving direct children", async () => {
    const parent = { ...allNodes[0], subtreeNodes: allNodes };

    const result = await folderResolvers.Folder.children(parent, undefined, ctx);

    expect((result as any[]).map(r => r.id)).not.toContain("grandchild");
  });

  test("propagates subtreeNodes to resolved children for deeper nesting", async () => {
    const parent = { ...allNodes[0], subtreeNodes: allNodes };

    const children = (await folderResolvers.Folder.children(parent, undefined, ctx)) as any[];

    expect(children[0].subtreeNodes).toBe(allNodes);
    expect(children[1].subtreeNodes).toBe(allNodes);
  });

  test("resolves grandchildren correctly from nested cache (no HTTP)", async () => {
    const parent = { ...allNodes[0], subtreeNodes: allNodes };
    const children = (await folderResolvers.Folder.children(parent, undefined, ctx)) as any[];
    const child1 = children.find((c: any) => c.id === "child1");

    const grandchildren = (await folderResolvers.Folder.children(child1, undefined, ctx)) as any[];

    expect(mockFetch).not.toHaveBeenCalled();
    expect(grandchildren).toHaveLength(1);
    expect(grandchildren[0].id).toBe("grandchild");
  });

  test("falls back to HTTP when parent has no subtreeNodes", async () => {
    fetchListOk([makeGoFolder({ id: "c1", path: "root.child1" })]);
    const parent = node("root", "root");

    const result = await folderResolvers.Folder.children(parent, undefined, ctx);

    expect(mockFetch).toHaveBeenCalledTimes(1);
    expect((result as any[])[0].id).toBe("c1");
  });

  test("HTTP fallback URL contains children=true and encoded parent path", async () => {
    fetchListOk([]);
    await folderResolvers.Folder.children(node("x", "a.b"), undefined, ctx);
    const [url] = mockFetch.mock.calls[0];
    expect(url).toContain("children=true");
    expect(url).toContain("rootPath=a.b");
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

  test("maps non-404 Go errors via GO_ERROR_CODES (403 → FORBIDDEN)", async () => {
    mockFetch.mockResolvedValueOnce({ ok: false, status: 403, statusText: "Forbidden" });
    await expect(
      folderResolvers.Query.folder(undefined, { orgId: org, id: folderId }, ctx),
    ).rejects.toThrow(expect.objectContaining({ extensions: { code: "FORBIDDEN" } }));
  });
});
