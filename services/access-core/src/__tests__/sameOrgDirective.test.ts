import { describe, test, expect, vi, beforeEach } from "vitest";
import { createYoga } from "graphql-yoga";

const { mockCanDo } = vi.hoisted(() => ({ mockCanDo: vi.fn() }));
const { mockFilterAllowedResourceIds } = vi.hoisted(() => ({ mockFilterAllowedResourceIds: vi.fn() }));

vi.mock("../db/queries/canDo", () => ({
  canDo: mockCanDo,
  filterAllowedResourceIds: mockFilterAllowedResourceIds,
  filterVisible: async (
    userId: string,
    orgId: string,
    action: string,
    resourceType: string,
    items: { id: string }[],
  ) => {
    const allowed = await mockFilterAllowedResourceIds(
      userId,
      orgId,
      action,
      resourceType,
      items.map((i) => i.id),
    );
    return items.filter((i) => allowed.has(i.id));
  },
}));
vi.mock("../config", () => ({ config: { goAssetUrl: "http://go-mock" } }));
vi.mock("../db/prisma", () => ({ prisma: {} }));

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
    olpEnabled: false,
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
  vi.resetAllMocks();
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
});
