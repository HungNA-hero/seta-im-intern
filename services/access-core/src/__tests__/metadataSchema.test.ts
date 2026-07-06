import { beforeEach, describe, expect, test, vi } from "vitest";
import { createYoga } from "graphql-yoga";
import { createCanDoMock } from "./helpers/canDoMock";

const { mockCanDo } = vi.hoisted(() => ({ mockCanDo: vi.fn() }));
const { mockFilterAllowedResourceIds } = vi.hoisted(() => ({ mockFilterAllowedResourceIds: vi.fn() }));

vi.mock("../db/queries/canDo", () =>
  createCanDoMock(mockCanDo, mockFilterAllowedResourceIds),
);
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

/** Builds a request context for assembled metadata schema tests. */
function metadataContext(
  overrides: Partial<GraphQLContext> = {},
): GraphQLContext {
  return {
    userId: "user-1",
    currentOrgId: "org-1",
    isMember: true,
    roles: ["viewer"],
    olpEnabled: false,
    ...overrides,
  };
}

/** Executes a GraphQL operation through Yoga so auth and org directives run before resolvers. */
async function executeMetadataOperation(
  query: string,
  context: GraphQLContext,
) {
  const response = await yoga.fetch(
    "http://test/graphql",
    {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ query }),
    },
    { injected: context },
  );
  return response.json() as Promise<{
    data?: Record<string, unknown>;
    errors?: { extensions?: { code?: string } }[];
  }>;
}

beforeEach(() => {
  vi.resetAllMocks();
  mockCanDo.mockResolvedValue({ allowed: true, reason: null });
  mockFilterAllowedResourceIds.mockImplementation(async (_u: string, _o: string, _a: string, _r: string, ids: string[]) => new Set(ids));
});

describe("metadata schema directives", () => {
  test("rejects org mismatch before policy and Go", async () => {
    const result = await executeMetadataOperation(
      `query { metadataItems(orgId: "org-2", folderId: "folder-1") { id } }`,
      metadataContext(),
    );

    expect(result.errors?.[0]?.extensions?.code).toBe("FORBIDDEN");
    expect(mockCanDo).not.toHaveBeenCalled();
    expect(mockFetch).not.toHaveBeenCalled();
  });

  test("rejects a non-member before policy and Go", async () => {
    const result = await executeMetadataOperation(
      `query { metadataItems(orgId: "org-1", folderId: "folder-1") { id } }`,
      metadataContext({ isMember: false }),
    );

    expect(result.errors?.[0]?.extensions?.code).toBe("FORBIDDEN");
    expect(mockCanDo).not.toHaveBeenCalled();
    expect(mockFetch).not.toHaveBeenCalled();
  });

  test("executes an authorized metadata list through the assembled schema", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      status: 200,
      json: async () => ({ status: "success", count: 0, items: [] }),
    });

    const result = await executeMetadataOperation(
      `query { metadataItems(orgId: "org-1", folderId: "folder-1") { id } }`,
      metadataContext(),
    );

    expect(result.errors).toBeUndefined();
    expect(result.data?.metadataItems).toEqual([]);
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  test("keeps empty update validation inside the registered mutation", async () => {
    const result = await executeMetadataOperation(
      `mutation { updateMetadata(orgId: "org-1", id: "meta-1", input: {}) { id } }`,
      metadataContext(),
    );

    expect(result.errors?.[0]?.extensions?.code).toBe("BAD_USER_INPUT");
    expect(mockCanDo).not.toHaveBeenCalled();
    expect(mockFetch).not.toHaveBeenCalled();
  });
});
