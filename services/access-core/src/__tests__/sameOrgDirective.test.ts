import { describe, test, expect, vi, beforeEach } from "vitest";
import { createYoga } from "graphql-yoga";
import { createCanDoMock } from "./helpers/canDoMock";

const { mockCanDo } = vi.hoisted(() => ({ mockCanDo: vi.fn() }));
const { mockFilterAllowedResourceIds } = vi.hoisted(() => ({ mockFilterAllowedResourceIds: vi.fn() }));
const { mockTrainerFindFirst } = vi.hoisted(() => ({ mockTrainerFindFirst: vi.fn() }));

vi.mock("../authz/decision", () =>
  createCanDoMock(mockCanDo, mockFilterAllowedResourceIds),
);
vi.mock("../config", () => ({
  config: { goAssetUrl: "http://go-mock", assetInternalApiToken: "test-internal-token" },
}));
vi.mock("../db/prisma", () => ({
  prisma: { user: { findFirst: mockTrainerFindFirst } },
}));

const mockFetch = vi.fn();
vi.stubGlobal("fetch", mockFetch);

import { schema } from "../graphql/schema";
import type { GraphQLContext } from "../graphql/context";

const yoga = createYoga({
  schema,
  logging: false,
  maskedErrors: false,
  context: ({ injected }: { injected: GraphQLContext }) => injected,
});

function ctx(overrides: Partial<GraphQLContext> = {}): GraphQLContext {
  return {
    userId: "user-1",
    currentOrgId: "org-1",
    isMember: true,
    roles: ["org_admin"],
    roleIds: [],
    olpEnabled: false,
    factMemo: new Map(),
    ...overrides,
  };
}

async function run(query: string, gqlCtx: GraphQLContext) {
  const res = await yoga.fetch(
    "http://test/graphql",
    {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ query }),
    },
    { injected: gqlCtx },
  );
  return res.json() as Promise<{
    data?: Record<string, unknown>;
    errors?: { extensions?: { code?: string } }[];
  }>;
}

beforeEach(() => {
  delete process.env.TRAINER_ADMIN_ENABLED;
  delete process.env.TRAINER_ADMIN_EXPIRES_AT;
  vi.resetAllMocks();
  mockTrainerFindFirst.mockResolvedValue(null);
  mockCanDo.mockResolvedValue({ allowed: true, reason: null });
  mockFilterAllowedResourceIds.mockImplementation(async (_u: string, _o: string, _a: string, _r: string, ids: string[]) => new Set(ids));
});

describe("@sameOrg directive", () => {
  test("rejects a query when orgId arg != authenticated org, before the resolver runs", async () => {
    const result = await run(
      `query { folderTree(orgId: "org-2") { id } }`,
      ctx({ currentOrgId: "org-1" }),
    );

    expect(result.errors?.[0]?.extensions?.code).toBe("FORBIDDEN");
    expect(mockCanDo).not.toHaveBeenCalled();
    expect(mockFetch).not.toHaveBeenCalled();
  });

  test("allows a query when orgId arg matches the authenticated org", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ folders: [] }),
    });

    const result = await run(
      `query { folderTree(orgId: "org-1") { id } }`,
      ctx({ currentOrgId: "org-1" }),
    );

    expect(result.errors).toBeUndefined();
  });

  test("resolves Root to Animals to Dogs with one Go request", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        folders: [
          {
            id: "root",
            org_id: "org-1",
            path: "root",
            name: "Root",
            created_by: "user-1",
            created_at: "2026-07-01T00:00:00Z",
            updated_at: "2026-07-01T00:00:00Z",
          },
          {
            id: "animals",
            org_id: "org-1",
            path: "root.animals",
            name: "Animals",
            created_by: "user-1",
            created_at: "2026-07-01T00:00:00Z",
            updated_at: "2026-07-01T00:00:00Z",
          },
          {
            id: "dogs",
            org_id: "org-1",
            path: "root.animals.dogs",
            name: "Dogs",
            created_by: "user-1",
            created_at: "2026-07-01T00:00:00Z",
            updated_at: "2026-07-01T00:00:00Z",
          },
        ],
      }),
    });

    const result = await run(
      `query {
        folderTree(orgId: "org-1") {
          id
          name
          children { id name children { id name } }
        }
      }`,
      ctx(),
    );
    const folders = result.data?.folderTree as Array<{
      id: string;
      children: Array<{ id: string; children: Array<{ id: string }> }>;
    }>;
    const root = folders.find((folder) => folder.id === "root");

    expect(result.errors).toBeUndefined();
    expect(root?.children[0].id).toBe("animals");
    expect(root?.children[0].children[0].id).toBe("dogs");
    expect(mockFetch).toHaveBeenCalledTimes(1);
    expect(mockFetch.mock.calls[0][0]).toContain("tree=true");
  });

  test("rejects a mutation when orgId arg != authenticated org", async () => {
    const result = await run(
      `mutation { createFolder(orgId: "org-2", name: "x") { id } }`,
      ctx({ currentOrgId: "org-1" }),
    );

    expect(result.errors?.[0]?.extensions?.code).toBe("FORBIDDEN");
    expect(mockCanDo).not.toHaveBeenCalled();
    expect(mockFetch).not.toHaveBeenCalled();
  });

  test("rejects when the request has no authenticated org (currentOrgId null)", async () => {
    const result = await run(
      `query { folderTree(orgId: "org-1") { id } }`,
      ctx({ currentOrgId: null }),
    );

    expect(result.errors?.[0]?.extensions?.code).toBe("FORBIDDEN");
    expect(mockCanDo).not.toHaveBeenCalled();
  });

  test("rejects createRole targeting an org the caller isn't a member of", async () => {
    const result = await run(
      `mutation { createRole(orgId: "org-2", code: "viewer2", name: "Viewer2") { id } }`,
      ctx({ currentOrgId: "org-1" }),
    );

    expect(result.errors?.[0]?.extensions?.code).toBe("FORBIDDEN");
  });

  test("rejects addOrgMember targeting an org the caller isn't a member of", async () => {
    const result = await run(
      `mutation { addOrgMember(orgId: "org-2", userId: "user-2") }`,
      ctx({ currentOrgId: "org-1" }),
    );

    expect(result.errors?.[0]?.extensions?.code).toBe("FORBIDDEN");
  });

  test("rejects assignRole targeting an org the caller isn't a member of", async () => {
    const result = await run(
      `mutation { assignRole(orgId: "org-2", userId: "user-2", roleId: "role-1") }`,
      ctx({ currentOrgId: "org-1" }),
    );

    expect(result.errors?.[0]?.extensions?.code).toBe("FORBIDDEN");
  });

  test("rejects revokeRole targeting an org the caller isn't a member of", async () => {
    const result = await run(
      `mutation { revokeRole(orgId: "org-2", userId: "user-2", roleId: "role-1") }`,
      ctx({ currentOrgId: "org-1" }),
    );

    expect(result.errors?.[0]?.extensions?.code).toBe("FORBIDDEN");
  });

  test.each([
    `mutation { createRole(orgId: "org-1", code: "viewer2", name: "Viewer2") { id } }`,
    `mutation { addOrgMember(orgId: "org-1", userId: "user-2") }`,
    `mutation { assignRole(orgId: "org-1", userId: "user-2", roleId: "role-1") }`,
    `mutation { revokeRole(orgId: "org-1", userId: "user-2", roleId: "role-1") }`,
  ])("rejects administrative mutation for an ordinary organization member", async (query) => {
    const result = await run(query, ctx({ roles: ["viewer"] }));
    expect(result.errors?.[0]?.extensions?.code).toBe("FORBIDDEN");
  });

  test("rejects global user lifecycle mutations unless the temporary trainer gate is active", async () => {
    const result = await run(
      `mutation { createUser(email: "new@example.com", displayName: "New User") { id } }`,
      ctx({ roles: ["viewer"] }),
    );
    expect(result.errors?.[0]?.extensions?.code).toBe("FORBIDDEN");
  });

  test("rejects global user lifecycle mutations in production even with an active temporary gate", async () => {
    const originalNodeEnv = process.env.NODE_ENV;
    process.env.NODE_ENV = "production";
    process.env.TRAINER_ADMIN_ENABLED = "true";
    process.env.TRAINER_ADMIN_EXPIRES_AT = "2099-01-01T00:00:00.000Z";
    mockTrainerFindFirst.mockResolvedValueOnce({ id: "user-1" });
    try {
      const result = await run(
        `mutation { createUser(email: "new@example.com", displayName: "New User") { id } }`,
        ctx(),
      );
      expect(result.errors?.[0]?.extensions?.code).toBe("FORBIDDEN");
    } finally {
      process.env.NODE_ENV = originalNodeEnv;
    }
  });

  test("does not expose a caller-supplied subject in canDo", async () => {
    const result = await run(
      `query { canDo(userId: "another-user", action: read, resourceType: folder, resourceId: "folder-1") { allowed } }`,
      ctx(),
    );
    expect(result.errors?.[0]).toBeDefined();
    expect(mockCanDo).not.toHaveBeenCalled();
  });

  test("evaluates canDo for the authenticated request actor", async () => {
    const result = await run(
      `query { canDo(action: read, resourceType: folder, resourceId: "folder-1") { allowed } }`,
      ctx({ userId: "current-user" }),
    );
    expect(result.errors).toBeUndefined();
    expect(mockCanDo).toHaveBeenCalledWith(
      "current-user",
      "read",
      "folder",
      "folder-1",
      "org-1",
      expect.objectContaining({
        preloaded: expect.objectContaining({
          userId: "current-user",
          orgId: "org-1",
        }),
      }),
    );
  });
});
