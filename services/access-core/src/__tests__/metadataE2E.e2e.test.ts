import type { FastifyInstance } from "fastify";
import { Client } from "pg";
import {
  afterAll,
  beforeAll,
  beforeEach,
  describe,
  expect,
  test,
  vi,
} from "vitest";
import { prisma } from "../db/prisma";
import { buildServer } from "../server";

const { mockCanDo } = vi.hoisted(() => ({
  mockCanDo: vi.fn(),
}));

// Policy is the only injected boundary; authentication and org membership use Access DB.
vi.mock("../db/queries/canDo", () => ({ canDo: mockCanDo }));

const ORG_ID = "00000000-0000-0000-0000-000000000010";
const OTHER_ORG_ID = "00000000-0000-0000-0000-000000000099";
const ROOT_FOLDER_ID = "10000000-0000-0000-0000-000000000000";
const USER_ID = "00000000-0000-0000-0000-000000000001";

interface GraphQLErrorResult {
  message?: string;
  extensions?: { code?: string };
}

interface GraphQLResult<T> {
  data?: T;
  errors?: GraphQLErrorResult[];
}

interface MetadataSummary {
  id: string;
  title: string;
  labels: string[];
}

let app: FastifyInstance;
let assetDb: Client;
let createdItemId = "";

/** Executes GraphQL through Fastify/Yoga with the same identity headers as production. */
async function queryGraphQL<T>(
  query: string,
  variables: Record<string, unknown>,
  userId: string | null = USER_ID,
  orgId: string | null = ORG_ID,
): Promise<GraphQLResult<T>> {
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
  };
  if (userId) headers["x-user-id"] = userId;
  if (orgId) headers["x-org-id"] = orgId;

  const response = await app.inject({
    method: "POST",
    url: "/graphql",
    headers,
    payload: { query, variables },
  });
  return response.json() as GraphQLResult<T>;
}

/** Returns the first GraphQL extension code for a failed operation. */
function firstErrorCode(result: GraphQLResult<unknown>): string | undefined {
  return result.errors?.[0]?.extensions?.code;
}

beforeAll(async () => {
  assetDb = new Client({
    connectionString:
      process.env.ASSET_DB_URL ??
      "postgresql://asset_user:asset_password@127.0.0.1:5433/asset_db",
  });
  await assetDb.connect();

  // Org (…010) and USER_ID's membership come from the committed access seed (V2).
  await assetDb.query("DELETE FROM metadata_items WHERE title LIKE 'E2E:%'");

  app = await buildServer();
});

afterAll(async () => {
  if (assetDb) {
    await assetDb.query("DELETE FROM metadata_items WHERE title LIKE 'E2E:%'");
  }
  if (app) await app.close();
  await prisma.$disconnect();
  await assetDb?.end();
});

beforeEach(() => {
  vi.restoreAllMocks();
  mockCanDo.mockReset();
  mockCanDo.mockResolvedValue({ allowed: true, reason: null });
});

describe("Folder tree regression", () => {
  test("KAN-34 resolves Root to Animals to Dogs with one Go request", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch");
    const result = await queryGraphQL<{
      folderTree: Array<{
        id: string;
        name: string;
        children: Array<{
          id: string;
          name: string;
          children: Array<{ id: string; name: string }>;
        }>;
      }>;
    }>(
      `query($orgId: ID!) {
        folderTree(orgId: $orgId) {
          id
          name
          children { id name children { id name } }
        }
      }`,
      { orgId: ORG_ID },
    );
    const root = result.data?.folderTree.find((folder) => folder.name === "Root");

    expect(result.errors).toBeUndefined();
    expect(root?.children[0].name).toBe("Animals");
    expect(root?.children[0].children[0].name).toBe("Dogs");
    expect(fetchSpy).toHaveBeenCalledTimes(1);
    expect(mockCanDo).toHaveBeenCalledTimes(1);
  });
});

describe("Metadata GraphQL to PostgreSQL E2E", () => {
  test("1. returns an empty metadata list", async () => {
    const result = await queryGraphQL<{ metadataItems: MetadataSummary[] }>(
      `query($orgId: ID!, $folderId: ID!) {
        metadataItems(orgId: $orgId, folderId: $folderId) { id title labels }
      }`,
      { orgId: ORG_ID, folderId: ROOT_FOLDER_ID },
    );

    expect(result.errors).toBeUndefined();
    expect(result.data?.metadataItems).toEqual([]);
  });

  test("2. creates metadata and persists fields plus audit values", async () => {
    const before = await assetDb.query("SELECT COUNT(*) FROM metadata_items");
    const result = await queryGraphQL<{
      createMetadata: MetadataSummary & {
        description: string;
        category: string;
        externalSource: string;
        externalId: string;
        metadataJson: string;
      };
    }>(
      `mutation($orgId: ID!, $input: CreateMetadataInput!) {
        createMetadata(orgId: $orgId, input: $input) {
          id title description labels category externalSource externalId metadataJson
        }
      }`,
      {
        orgId: ORG_ID,
        input: {
          folderId: ROOT_FOLDER_ID,
          title: "E2E: New item",
          description: "A test description",
          labels: ["test", "e2e"],
          category: "e2e_test",
          externalSource: "e2e_source",
          externalId: "e2e-001",
          metadataJson: JSON.stringify({ key: "value" }),
        },
      },
    );

    expect(result.errors).toBeUndefined();
    expect(result.data?.createMetadata).toMatchObject({
      title: "E2E: New item",
      labels: ["test", "e2e"],
      metadataJson: '{"key":"value"}',
    });
    createdItemId = result.data?.createMetadata.id ?? "";

    const persisted = await assetDb.query(
      `SELECT title, labels, metadata_json, created_by, updated_by
       FROM metadata_items WHERE id = $1`,
      [createdItemId],
    );
    expect(persisted.rows).toHaveLength(1);
    expect(persisted.rows[0]).toMatchObject({
      title: "E2E: New item",
      labels: ["test", "e2e"],
      metadata_json: { key: "value" },
      created_by: USER_ID,
      updated_by: null,
    });
    expect(Number((await assetDb.query("SELECT COUNT(*) FROM metadata_items")).rows[0].count)).toBe(
      Number(before.rows[0].count) + 1,
    );
  });

  test("3. lists the created item", async () => {
    const result = await queryGraphQL<{ metadataItems: MetadataSummary[] }>(
      `query($orgId: ID!, $folderId: ID!) {
        metadataItems(orgId: $orgId, folderId: $folderId) { id title labels }
      }`,
      { orgId: ORG_ID, folderId: ROOT_FOLDER_ID },
    );

    expect(result.errors).toBeUndefined();
    expect(result.data?.metadataItems).toEqual([
      expect.objectContaining({ id: createdItemId, title: "E2E: New item" }),
    ]);
  });

  test("4. returns camelCase metadata detail", async () => {
    const result = await queryGraphQL<{
      metadataItem: {
        id: string;
        externalSource: string;
        externalId: string;
        metadataJson: string;
      } | null;
    }>(
      `query($orgId: ID!, $id: ID!) {
        metadataItem(orgId: $orgId, id: $id) {
          id externalSource externalId metadataJson
        }
      }`,
      { orgId: ORG_ID, id: createdItemId },
    );

    expect(result.errors).toBeUndefined();
    expect(result.data?.metadataItem).toMatchObject({
      externalSource: "e2e_source",
      externalId: "e2e-001",
      metadataJson: '{"key":"value"}',
    });
  });

  test("5. updates metadata and persists audit values", async () => {
    const before = await assetDb.query(
      "SELECT updated_at FROM metadata_items WHERE id = $1",
      [createdItemId],
    );
    const result = await queryGraphQL<{
      updateMetadata: MetadataSummary & { metadataJson: string; updatedAt: string };
    }>(
      `mutation($orgId: ID!, $id: ID!, $input: UpdateMetadataInput!) {
        updateMetadata(orgId: $orgId, id: $id, input: $input) {
          id title labels metadataJson updatedAt
        }
      }`,
      {
        orgId: ORG_ID,
        id: createdItemId,
        input: {
          title: "E2E: Updated item",
          labels: ["updated"],
          metadataJson: JSON.stringify({ updated: true }),
        },
      },
    );

    expect(result.errors).toBeUndefined();
    expect(result.data?.updateMetadata).toMatchObject({
      title: "E2E: Updated item",
      labels: ["updated"],
      metadataJson: '{"updated":true}',
    });
    const persisted = await assetDb.query(
      `SELECT title, labels, metadata_json, updated_by, updated_at
       FROM metadata_items WHERE id = $1`,
      [createdItemId],
    );
    expect(persisted.rows[0]).toMatchObject({
      title: "E2E: Updated item",
      labels: ["updated"],
      metadata_json: { updated: true },
      updated_by: USER_ID,
    });
    expect(persisted.rows[0].updated_at.getTime()).toBeGreaterThanOrEqual(
      before.rows[0].updated_at.getTime(),
    );
  });

  test("6. clears explicitly-null nullable fields", async () => {
    const result = await queryGraphQL<{
      updateMetadata: { description: string | null; category: string | null };
    }>(
      `mutation($orgId: ID!, $id: ID!, $input: UpdateMetadataInput!) {
        updateMetadata(orgId: $orgId, id: $id, input: $input) { description category }
      }`,
      {
        orgId: ORG_ID,
        id: createdItemId,
        input: { description: null, category: null },
      },
    );

    expect(result.errors).toBeUndefined();
    expect(result.data?.updateMetadata).toEqual({ description: null, category: null });
    const persisted = await assetDb.query(
      "SELECT description, category FROM metadata_items WHERE id = $1",
      [createdItemId],
    );
    expect(persisted.rows[0]).toEqual({ description: null, category: null });
  });

  test("7. preserves omitted fields", async () => {
    const result = await queryGraphQL<{
      updateMetadata: MetadataSummary & { notes: string };
    }>(
      `mutation($orgId: ID!, $id: ID!, $input: UpdateMetadataInput!) {
        updateMetadata(orgId: $orgId, id: $id, input: $input) { title labels notes }
      }`,
      {
        orgId: ORG_ID,
        id: createdItemId,
        input: { notes: "E2E note" },
      },
    );

    expect(result.errors).toBeUndefined();
    expect(result.data?.updateMetadata).toMatchObject({
      title: "E2E: Updated item",
      labels: ["updated"],
      notes: "E2E note",
    });
  });

  test("8. rejects missing, wrong-org, and soft-deleted resources", async () => {
    const missingFolder = await queryGraphQL<unknown>(
      `query($orgId: ID!, $folderId: ID!) {
        metadataItems(orgId: $orgId, folderId: $folderId) { id }
      }`,
      {
        orgId: ORG_ID,
        folderId: "10000000-0000-0000-0000-000000000099",
      },
    );
    expect(firstErrorCode(missingFolder)).toBe("NOT_FOUND");

    const wrongOrg = await queryGraphQL<unknown>(
      `query($orgId: ID!, $id: ID!) { metadataItem(orgId: $orgId, id: $id) { id } }`,
      { orgId: OTHER_ORG_ID, id: createdItemId },
      USER_ID,
      OTHER_ORG_ID,
    );
    expect(firstErrorCode(wrongOrg)).toBe("FORBIDDEN");

    await assetDb.query(
      "UPDATE metadata_items SET deleted_at = now() WHERE id = $1",
      [createdItemId],
    );
    try {
      const softDeleted = await queryGraphQL<{ metadataItem: null }>(
        `query($orgId: ID!, $id: ID!) { metadataItem(orgId: $orgId, id: $id) { id } }`,
        { orgId: ORG_ID, id: createdItemId },
      );
      expect(softDeleted.errors).toBeUndefined();
      expect(softDeleted.data?.metadataItem).toBeNull();
    } finally {
      await assetDb.query(
        "UPDATE metadata_items SET deleted_at = NULL WHERE id = $1",
        [createdItemId],
      );
    }
  });

  test("9. rejects invalid title, external pair, and metadata JSON", async () => {
    const mutation = `mutation($orgId: ID!, $input: CreateMetadataInput!) {
      createMetadata(orgId: $orgId, input: $input) { id }
    }`;
    const invalidTitle = await queryGraphQL<unknown>(mutation, {
      orgId: ORG_ID,
      input: { folderId: ROOT_FOLDER_ID, title: " " },
    });
    const invalidPair = await queryGraphQL<unknown>(mutation, {
      orgId: ORG_ID,
      input: {
        folderId: ROOT_FOLDER_ID,
        title: "E2E: Invalid pair",
        externalSource: "source-only",
      },
    });
    const invalidJson = await queryGraphQL<unknown>(mutation, {
      orgId: ORG_ID,
      input: {
        folderId: ROOT_FOLDER_ID,
        title: "E2E: Invalid JSON",
        metadataJson: "not-json",
      },
    });

    expect(firstErrorCode(invalidTitle)).toBe("BAD_USER_INPUT");
    expect(firstErrorCode(invalidPair)).toBe("BAD_USER_INPUT");
    expect(firstErrorCode(invalidJson)).toBe("BAD_USER_INPUT");
  });

  test("10. rejects duplicate external identity", async () => {
    const result = await queryGraphQL<unknown>(
      `mutation($orgId: ID!, $input: CreateMetadataInput!) {
        createMetadata(orgId: $orgId, input: $input) { id }
      }`,
      {
        orgId: ORG_ID,
        input: {
          folderId: ROOT_FOLDER_ID,
          title: "E2E: Duplicate identity",
          externalSource: "e2e_source",
          externalId: "e2e-001",
        },
      },
    );

    expect(firstErrorCode(result)).toBe("CONFLICT");
  });

  test("11. distinguishes missing requester from missing current org", async () => {
    const query = `query($orgId: ID!, $folderId: ID!) {
      metadataItems(orgId: $orgId, folderId: $folderId) { id }
    }`;
    const missingRequester = await queryGraphQL<unknown>(
      query,
      { orgId: ORG_ID, folderId: ROOT_FOLDER_ID },
      null,
      ORG_ID,
    );
    const missingCurrentOrg = await queryGraphQL<unknown>(
      query,
      { orgId: ORG_ID, folderId: ROOT_FOLDER_ID },
      USER_ID,
      null,
    );

    expect(firstErrorCode(missingRequester)).toBe("UNAUTHENTICATED");
    expect(firstErrorCode(missingCurrentOrg)).toBe("FORBIDDEN");
  });

  test("12. policy denial performs no Go request and leaves DB unchanged", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch");
    const countBefore = await assetDb.query("SELECT COUNT(*) FROM metadata_items");
    const rowBefore = await assetDb.query(
      "SELECT title, updated_at FROM metadata_items WHERE id = $1",
      [createdItemId],
    );

    mockCanDo.mockResolvedValueOnce({ allowed: false, reason: "deny read" });
    const deniedRead = await queryGraphQL<unknown>(
      `query($orgId: ID!, $folderId: ID!) {
        metadataItems(orgId: $orgId, folderId: $folderId) { id }
      }`,
      { orgId: ORG_ID, folderId: ROOT_FOLDER_ID },
    );
    expect(firstErrorCode(deniedRead)).toBe("FORBIDDEN");

    mockCanDo.mockResolvedValueOnce({ allowed: false, reason: "deny create" });
    const deniedCreate = await queryGraphQL<unknown>(
      `mutation($orgId: ID!, $input: CreateMetadataInput!) {
        createMetadata(orgId: $orgId, input: $input) { id }
      }`,
      {
        orgId: ORG_ID,
        input: { folderId: ROOT_FOLDER_ID, title: "E2E: Denied create" },
      },
    );
    expect(firstErrorCode(deniedCreate)).toBe("FORBIDDEN");

    mockCanDo.mockResolvedValueOnce({ allowed: false, reason: "deny update" });
    const deniedUpdate = await queryGraphQL<unknown>(
      `mutation($orgId: ID!, $id: ID!, $input: UpdateMetadataInput!) {
        updateMetadata(orgId: $orgId, id: $id, input: $input) { id }
      }`,
      {
        orgId: ORG_ID,
        id: createdItemId,
        input: { title: "E2E: Denied update" },
      },
    );
    expect(firstErrorCode(deniedUpdate)).toBe("FORBIDDEN");

    mockCanDo.mockRejectedValueOnce(new Error("sensitive policy failure"));
    const policyFailure = await queryGraphQL<unknown>(
      `query($orgId: ID!, $folderId: ID!) {
        metadataItems(orgId: $orgId, folderId: $folderId) { id }
      }`,
      { orgId: ORG_ID, folderId: ROOT_FOLDER_ID },
    );
    expect(firstErrorCode(policyFailure)).toBe("INTERNAL_SERVER_ERROR");
    expect(policyFailure.errors?.[0]?.message).toBe("Unexpected error.");

    expect(fetchSpy).not.toHaveBeenCalled();
    expect(await assetDb.query("SELECT COUNT(*) FROM metadata_items")).toMatchObject({
      rows: [{ count: countBefore.rows[0].count }],
    });
    const rowAfter = await assetDb.query(
      "SELECT title, updated_at FROM metadata_items WHERE id = $1",
      [createdItemId],
    );
    expect(rowAfter.rows[0]).toEqual(rowBefore.rows[0]);
  });
});
