import { describe, test, expect, vi, beforeEach } from "vitest";
import { createYoga } from "graphql-yoga";

const { mockCanDo } = vi.hoisted(() => ({ mockCanDo: vi.fn() }));

vi.mock("../db/queries/canDo", () => ({ canDo: mockCanDo }));
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
    expect(mockCanDo).toHaveBeenCalledTimes(1);
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
