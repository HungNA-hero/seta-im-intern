import { beforeEach, describe, expect, test, vi } from "vitest";
import { createYoga } from "graphql-yoga";
import { createCanDoMock } from "./helpers/canDoMock";

const { mockCanDo } = vi.hoisted(() => ({ mockCanDo: vi.fn() }));
const { mockFilterAllowedResourceIds } = vi.hoisted(() => ({ mockFilterAllowedResourceIds: vi.fn() }));

vi.mock("../authz/decision", () => createCanDoMock(mockCanDo, mockFilterAllowedResourceIds));
vi.mock("../config", () => ({
  config: { goAssetUrl: "http://go-mock", assetInternalApiToken: "test-internal-token" },
  ASSET_FETCH_TIMEOUT_MS: 3000,
}));
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

function context(overrides: Partial<GraphQLContext> = {}): GraphQLContext {
  return {
    userId: "00000000-0000-4000-8000-0000000000b0",
    currentOrgId: "00000000-0000-4000-8000-0000000000a0",
    isMember: true,
    roles: ["viewer"],
    olpEnabled: false,
    ...overrides,
  };
}

async function execute(query: string, injected: GraphQLContext) {
  const response = await yoga.fetch(
    "http://test/graphql",
    { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ query }) },
    { injected },
  );
  return response.json() as Promise<{
    data?: Record<string, unknown>;
    errors?: Array<{ extensions?: { code?: string } }>;
  }>;
}

beforeEach(() => {
  vi.resetAllMocks();
  mockCanDo.mockResolvedValue({ allowed: true, reason: null });
  mockFilterAllowedResourceIds.mockImplementation(
    async (_userId: string, _orgId: string, _action: string, _resourceType: string, ids: string[]) => new Set(ids),
  );
});

describe("recycleBin GraphQL query", () => {
  test("enforces org membership and context before calling Asset Core", async () => {
    const result = await execute(
      `query { recycleBin(orgId: "org-2", input: { first: 1 }) { nodes { resourceId } } }`,
      context(),
    );

    expect(result.errors?.[0]?.extensions?.code).toBe("FORBIDDEN");
    expect(mockFetch).not.toHaveBeenCalled();
  });

  test("returns only read-authorized roots and preserves the connection contract", async () => {
    mockFilterAllowedResourceIds.mockResolvedValueOnce(new Set(["folder-allowed"]));
    mockFetch.mockResolvedValueOnce({
      ok: true,
      status: 200,
      json: async () => ({
        status: "success",
        count: 2,
        hasMore: false,
        entries: [
          {
            lifecycle_unit_id: "00000000-0000-4000-8000-000000000001",
            resource_type: "FOLDER",
            resource_id: "folder-denied",
            display_name: "hidden",
            root_folder_path: "root.000000000000400080000000000000a0",
            deleted_at: "2026-08-12T10:00:00Z",
          },
          {
            lifecycle_unit_id: "00000000-0000-4000-8000-000000000002",
            resource_type: "FOLDER",
            resource_id: "folder-allowed",
            display_name: "visible",
            root_folder_path: "root.000000000000400080000000000000a0",
            deleted_at: "2026-08-12T09:00:00Z",
          },
        ],
      }),
    });

    const result = await execute(
      `query { recycleBin(orgId: "00000000-0000-4000-8000-0000000000a0", input: { first: 1 }) { nodes { resourceId displayName resourceType } pageInfo { endCursor hasNextPage } } }`,
      context(),
    );

    expect(result.errors).toBeUndefined();
    expect(result.data?.recycleBin).toMatchObject({
      nodes: [{ resourceId: "folder-allowed", displayName: "visible", resourceType: "FOLDER" }],
      pageInfo: { hasNextPage: false },
    });
    expect(JSON.stringify(result.data)).not.toContain("folder-denied");
  });
});
