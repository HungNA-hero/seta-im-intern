import { Client } from "pg";
import type { FastifyInstance } from "fastify";
import { afterAll, afterEach, beforeAll, describe, expect, test } from "vitest";
import { prisma } from "../db/prisma";
import { buildServer } from "../server";

const ORG_ID = "00000000-0000-0000-0000-000000000010";
const USER_ADMIN = "00000000-0000-0000-0000-000000000001";
const ROOT_FOLDER_ID = "10000000-0000-0000-0000-000000000000";
const MISSING_FOLDER_ID = "10000000-0000-0000-0000-000000000074";
const CURSOR_ITEM_ID = "20000000-0000-0000-0000-000000000074";
const NON_EMPTY_PARENT_ID = "10000000-0000-0000-0000-000000000075";
const NON_EMPTY_CHILD_ID = "10000000-0000-0000-0000-000000000076";
const MISSING_METADATA_ID = "20000000-0000-0000-0000-000000000075";
const CONFLICT_FOLDER_ID = "10000000-0000-0000-0000-000000000077";
const CONFLICT_FOLDER_NAME = "KAN-74 duplicate folder name";
const CYCLE_PARENT_ID = "10000000-0000-0000-0000-000000000078";
const CYCLE_CHILD_ID = "10000000-0000-0000-0000-000000000079";
const ACTIVE_RESTORE_FOLDER_ID = "10000000-0000-0000-0000-000000000080";
const DELETED_RESTORE_PARENT_ID = "10000000-0000-0000-0000-000000000081";
const DELETED_RESTORE_CHILD_ID = "10000000-0000-0000-0000-000000000082";
const DELETION_JOB_FOLDER_ID = "10000000-0000-0000-0000-000000000083";
const DELETION_PREVIEW_CHILD_ID = "10000000-0000-0000-0000-000000000084";
const MISSING_DELETION_JOB_ID = "30000000-0000-0000-0000-000000000074";
const QUEUED_DELETION_JOB_ID = "30000000-0000-0000-0000-000000000075";
const RUNNING_DELETION_JOB_ID = "30000000-0000-0000-0000-000000000076";
const IDENTITY_METADATA_ID = "20000000-0000-0000-0000-000000000076";
const ACTIVE_RESTORE_METADATA_ID = "20000000-0000-0000-0000-000000000077";
const DELETED_RESTORE_METADATA_ID = "20000000-0000-0000-0000-000000000078";
const DELETED_METADATA_FOLDER_ID = "10000000-0000-0000-0000-000000000085";
const IDENTITY_EXTERNAL_SOURCE = "kan74-source";
const IDENTITY_EXTERNAL_ID = "kan74-identity";
const TRACE_ID = "74a74a74a74a74a74a74a74a74a74a74";

interface PublicError {
  message?: string;
  extensions?: Record<string, unknown>;
}

interface GraphQLResult<T> {
  data?: T | null;
  errors?: PublicError[];
}

let app: FastifyInstance;
let assetDb: Client;

/** Sends a public GraphQL operation through the production request context. */
async function queryGraphQL<T>(query: string, variables: Record<string, unknown>): Promise<GraphQLResult<T>> {
  const response = await app.inject({
    method: "POST",
    url: "/graphql",
    headers: {
      "Content-Type": "application/json",
      "x-user-id": USER_ADMIN,
      "x-org-id": ORG_ID,
      traceparent: `00-${TRACE_ID}-0123456789abcdef-01`,
      "x-request-id": "kan-74-error-contract-e2e",
    },
    payload: { query, variables },
  });
  return response.json() as GraphQLResult<T>;
}

/** Verifies the complete public contract and rejects implementation diagnostics. */
function expectAssetError(
  result: GraphQLResult<unknown>,
  expected: { code: string; number: number; message: string },
): void {
  const error = result.errors?.[0];
  expect(error?.message).toBe(expected.message);
  expect(error?.extensions).toEqual({
    code: expected.code,
    number: expected.number,
    traceId: TRACE_ID,
    service: "asset-core",
  });
  expect(JSON.stringify(error)).not.toMatch(/postgres|prisma|sqlstate|gorm|stack|localhost|internal\/api/i);
}

async function removeCursorFixture(): Promise<void> {
  await assetDb.query("DELETE FROM metadata_items WHERE id = $1", [CURSOR_ITEM_ID]);
}

async function removeMetadataFixtures(): Promise<void> {
  await assetDb.query("DELETE FROM metadata_items WHERE id = ANY($1::uuid[])", [
    [IDENTITY_METADATA_ID, ACTIVE_RESTORE_METADATA_ID, DELETED_RESTORE_METADATA_ID],
  ]);
}

async function removeFolderFixtures(): Promise<void> {
  await assetDb.query("DELETE FROM folder_deletion_jobs WHERE id = ANY($1::uuid[]) OR root_folder_id = $2", [
    [QUEUED_DELETION_JOB_ID, RUNNING_DELETION_JOB_ID],
    DELETION_JOB_FOLDER_ID,
  ]);
  await assetDb.query("DELETE FROM folders WHERE id = ANY($1::uuid[])", [
    [
      NON_EMPTY_CHILD_ID,
      NON_EMPTY_PARENT_ID,
      CONFLICT_FOLDER_ID,
      CYCLE_CHILD_ID,
      CYCLE_PARENT_ID,
      ACTIVE_RESTORE_FOLDER_ID,
      DELETED_RESTORE_CHILD_ID,
      DELETED_RESTORE_PARENT_ID,
      DELETION_PREVIEW_CHILD_ID,
      DELETION_JOB_FOLDER_ID,
      DELETED_METADATA_FOLDER_ID,
    ],
  ]);
}

beforeAll(async () => {
  assetDb = new Client({
    connectionString: process.env.ASSET_DB_URL ?? "postgresql://asset_user:asset_password@127.0.0.1:5433/asset_db",
  });
  await assetDb.connect();
  app = await buildServer();
  await app.ready();
});

afterEach(async () => {
  await removeCursorFixture();
  await removeMetadataFixtures();
  await removeFolderFixtures();
});

afterAll(async () => {
  await removeCursorFixture();
  await removeMetadataFixtures();
  await removeFolderFixtures();
  if (app) await app.close();
  await assetDb.end();
  await prisma.$disconnect();
});

describe("KAN-74 public Asset error contract E2E", () => {
  test("EC-01 forwards an Asset folder-not-found envelope with its trace", async () => {
    const result = await queryGraphQL<unknown>(
      `query($orgId: ID!, $folderId: ID!) {
        metadataItems(orgId: $orgId, folderId: $folderId) { id }
      }`,
      { orgId: ORG_ID, folderId: MISSING_FOLDER_ID },
    );

    expect(result.data).toBeNull();
    expectAssetError(result, {
      code: "FOLDER_NOT_FOUND",
      number: 3001,
      message: "Folder not found",
    });
    expect(JSON.stringify(result.errors)).not.toContain(MISSING_FOLDER_ID);
  });

  test("EC-02 forwards an Asset stale-cursor envelope with its trace", async () => {
    await assetDb.query(
      `INSERT INTO metadata_items
         (id, folder_id, title, labels, metadata_json, created_by, updated_by, updated_at)
       VALUES ($1, $2, 'KAN-74 cursor fixture', ARRAY['kan74-e2e'], '{}'::jsonb, $3, $3, now())`,
      [CURSOR_ITEM_ID, ROOT_FOLDER_ID, USER_ADMIN],
    );

    const operation = `query($orgId: ID!, $input: MetadataConnectionSearchInput!) {
      searchMetadataConnection(orgId: $orgId, input: $input) {
        nodes { id }
        pageInfo { endCursor }
      }
    }`;
    const variables = {
      orgId: ORG_ID,
      input: {
        folderId: ROOT_FOLDER_ID,
        query: "KAN-74 cursor fixture",
        labels: ["kan74-e2e"],
        first: 1,
      },
    };
    const first = await queryGraphQL<{
      searchMetadataConnection: { pageInfo: { endCursor: string | null } };
    }>(operation, variables);
    expect(first.errors).toBeUndefined();
    const cursor = first.data?.searchMetadataConnection.pageInfo.endCursor;
    expect(cursor).toEqual(expect.any(String));

    await assetDb.query("UPDATE metadata_items SET deleted_at = now() WHERE id = $1", [CURSOR_ITEM_ID]);
    const stale = await queryGraphQL<unknown>(operation, {
      ...variables,
      input: { ...variables.input, after: cursor },
    });

    expect(stale.data).toBeNull();
    expectAssetError(stale, {
      code: "CURSOR_INVALID",
      number: 1003,
      message: "Pagination cursor is malformed or stale",
    });
  });

  test("EC-03 forwards an Asset non-empty-folder envelope with its trace", async () => {
    await assetDb.query(
      `INSERT INTO folders (id, org_id, path, name, created_by, updated_by)
       VALUES
         ($1, $3, 'root.kan74parent', 'KAN-74 non-empty parent', $4, $4),
         ($2, $3, 'root.kan74parent.child', 'KAN-74 active child', $4, $4)`,
      [NON_EMPTY_PARENT_ID, NON_EMPTY_CHILD_ID, ORG_ID, USER_ADMIN],
    );

    const result = await queryGraphQL<unknown>(
      `mutation($orgId: ID!, $id: ID!) {
        deleteFolder(orgId: $orgId, id: $id)
      }`,
      { orgId: ORG_ID, id: NON_EMPTY_PARENT_ID },
    );

    expect(result.data).toBeNull();
    expectAssetError(result, {
      code: "FOLDER_NOT_EMPTY",
      number: 3004,
      message: "Folder contains active descendants or metadata",
    });
    const persisted = await assetDb.query<{ count: number }>(
      "SELECT COUNT(*)::int AS count FROM folders WHERE id = ANY($1::uuid[]) AND deleted_at IS NULL",
      [[NON_EMPTY_PARENT_ID, NON_EMPTY_CHILD_ID]],
    );
    expect(persisted.rows[0].count).toBe(2);
  });

  test("EC-04 forwards an Asset missing-metadata envelope with its trace", async () => {
    const result = await queryGraphQL<unknown>(
      `mutation($orgId: ID!, $id: ID!) {
        deleteMetadata(orgId: $orgId, id: $id)
      }`,
      { orgId: ORG_ID, id: MISSING_METADATA_ID },
    );

    expect(result.data).toBeNull();
    expectAssetError(result, {
      code: "METADATA_NOT_FOUND",
      number: 4001,
      message: "Metadata item not found",
    });
    expect(JSON.stringify(result.errors)).not.toContain(MISSING_METADATA_ID);
  });

  test("EC-05 forwards an Asset duplicate-folder envelope with its trace", async () => {
    await assetDb.query(
      `INSERT INTO folders (id, org_id, path, name, created_by, updated_by)
       VALUES ($1, $2, 'root.kan74conflict', $3, $4, $4)`,
      [CONFLICT_FOLDER_ID, ORG_ID, CONFLICT_FOLDER_NAME, USER_ADMIN],
    );

    const result = await queryGraphQL<unknown>(
      `mutation($orgId: ID!, $name: String!, $parentPath: String) {
        createFolder(orgId: $orgId, name: $name, parentPath: $parentPath) {
          id
        }
      }`,
      {
        orgId: ORG_ID,
        name: CONFLICT_FOLDER_NAME,
        parentPath: "root",
      },
    );

    expect(result.data).toBeNull();
    expectAssetError(result, {
      code: "FOLDER_NAME_CONFLICT",
      number: 3003,
      message: "A folder with this name already exists at this location",
    });
    expect(JSON.stringify(result.errors)).not.toContain(CONFLICT_FOLDER_NAME);
    const persisted = await assetDb.query<{ count: number }>(
      "SELECT COUNT(*)::int AS count FROM folders WHERE org_id = $1 AND name = $2 AND deleted_at IS NULL",
      [ORG_ID, CONFLICT_FOLDER_NAME],
    );
    expect(persisted.rows[0].count).toBe(1);
  });

  test("forwards the Asset folder-cycle envelope without changing folder paths", async () => {
    await assetDb.query(
      `INSERT INTO folders (id, org_id, path, name, created_by, updated_by)
       VALUES
         ($1, $3, 'root.kan74cycle', 'KAN-74 cycle parent', $4, $4),
         ($2, $3, 'root.kan74cycle.child', 'KAN-74 cycle child', $4, $4)`,
      [CYCLE_PARENT_ID, CYCLE_CHILD_ID, ORG_ID, USER_ADMIN],
    );
    const before = await assetDb.query<{ id: string; path: string }>(
      "SELECT id::text, path::text FROM folders WHERE id = ANY($1::uuid[]) ORDER BY path",
      [[CYCLE_PARENT_ID, CYCLE_CHILD_ID]],
    );

    const result = await queryGraphQL<unknown>(
      `mutation($orgId: ID!, $id: ID!, $destinationParentId: ID) {
        moveFolder(orgId: $orgId, id: $id, destinationParentId: $destinationParentId) {
          id
        }
      }`,
      {
        orgId: ORG_ID,
        id: CYCLE_PARENT_ID,
        destinationParentId: CYCLE_CHILD_ID,
      },
    );

    expect(result.data).toBeNull();
    expectAssetError(result, {
      code: "FOLDER_CYCLE_DETECTED",
      number: 3005,
      message: "Move would create a cycle",
    });
    const after = await assetDb.query<{ id: string; path: string }>(
      "SELECT id::text, path::text FROM folders WHERE id = ANY($1::uuid[]) ORDER BY path",
      [[CYCLE_PARENT_ID, CYCLE_CHILD_ID]],
    );
    expect(after.rows).toEqual(before.rows);
  });

  test("forwards the Asset parent-deleted restore envelope without reviving the child", async () => {
    await assetDb.query(
      `INSERT INTO folders (id, org_id, path, name, created_by, updated_by, deleted_at)
       VALUES
         ($1, $3, 'root.kan74restoreparent', 'KAN-74 deleted parent', $4, $4, now()),
         ($2, $3, 'root.kan74restoreparent.child', 'KAN-74 deleted child', $4, $4, now())`,
      [DELETED_RESTORE_PARENT_ID, DELETED_RESTORE_CHILD_ID, ORG_ID, USER_ADMIN],
    );

    const result = await queryGraphQL<unknown>(
      `mutation($orgId: ID!, $id: ID!) {
        restoreFolder(orgId: $orgId, id: $id) { id }
      }`,
      { orgId: ORG_ID, id: DELETED_RESTORE_CHILD_ID },
    );

    expect(result.data).toBeNull();
    expectAssetError(result, {
      code: "FOLDER_PARENT_DELETED",
      number: 3006,
      message: "Parent folder is deleted or missing",
    });
    const persisted = await assetDb.query<{ count: number }>(
      "SELECT COUNT(*)::int AS count FROM folders WHERE id = ANY($1::uuid[]) AND deleted_at IS NOT NULL",
      [[DELETED_RESTORE_PARENT_ID, DELETED_RESTORE_CHILD_ID]],
    );
    expect(persisted.rows[0].count).toBe(2);
  });

  test("forwards the Asset already-active restore envelope without changing the row", async () => {
    await assetDb.query(
      `INSERT INTO folders (id, org_id, path, name, created_by, updated_by)
       VALUES ($1, $2, 'root.kan74activerestore', 'KAN-74 active restore', $3, $3)`,
      [ACTIVE_RESTORE_FOLDER_ID, ORG_ID, USER_ADMIN],
    );

    const result = await queryGraphQL<unknown>(
      `mutation($orgId: ID!, $id: ID!) {
        restoreFolder(orgId: $orgId, id: $id) { id }
      }`,
      { orgId: ORG_ID, id: ACTIVE_RESTORE_FOLDER_ID },
    );

    expect(result.data).toBeNull();
    expectAssetError(result, {
      code: "FOLDER_NOT_DELETED",
      number: 3012,
      message: "Folder is already active",
    });
    const persisted = await assetDb.query<{ count: number }>(
      "SELECT COUNT(*)::int AS count FROM folders WHERE id = $1 AND deleted_at IS NULL",
      [ACTIVE_RESTORE_FOLDER_ID],
    );
    expect(persisted.rows[0].count).toBe(1);
  });

  test("forwards the Asset stale-deletion-preview envelope", async () => {
    await assetDb.query(
      `INSERT INTO folders (id, org_id, path, name, created_by, updated_by)
       VALUES ($1, $2, 'root.kan74preview', 'KAN-74 stale preview', $3, $3)`,
      [DELETION_JOB_FOLDER_ID, ORG_ID, USER_ADMIN],
    );

    const preview = await queryGraphQL<{
      previewFolderDeletion: { id: string; confirmationToken: string };
    }>(
      `mutation($orgId: ID!, $folderId: ID!) {
        previewFolderDeletion(orgId: $orgId, folderId: $folderId) {
          id
          confirmationToken
        }
      }`,
      { orgId: ORG_ID, folderId: DELETION_JOB_FOLDER_ID },
    );
    expect(preview.errors).toBeUndefined();
    const previewResult = preview.data?.previewFolderDeletion;
    expect(previewResult).toBeDefined();
    if (!previewResult) throw new Error("Missing KAN-74 deletion preview fixture");

    await assetDb.query(
      `INSERT INTO folders (id, org_id, path, name, created_by, updated_by)
       VALUES ($1, $2, 'root.kan74preview.child', 'KAN-74 preview mutation', $3, $3)`,
      [DELETION_PREVIEW_CHILD_ID, ORG_ID, USER_ADMIN],
    );

    const result = await queryGraphQL<unknown>(
      `mutation($orgId: ID!, $folderId: ID!, $previewId: ID!, $confirmationToken: String!) {
        confirmFolderDeletion(
          orgId: $orgId
          folderId: $folderId
          previewId: $previewId
          confirmationToken: $confirmationToken
        ) { id }
      }`,
      {
        orgId: ORG_ID,
        folderId: DELETION_JOB_FOLDER_ID,
        previewId: previewResult.id,
        confirmationToken: previewResult.confirmationToken,
      },
    );

    expect(result.data).toBeNull();
    expectAssetError(result, {
      code: "DELETION_PREVIEW_STALE",
      number: 3008,
      message: "Folder deletion preview is stale; request a new preview",
    });
  });

  test("forwards the Asset active-deletion-job envelope", async () => {
    await assetDb.query(
      `INSERT INTO folders (id, org_id, path, name, created_by, updated_by)
       VALUES ($1, $2, 'root.kan74job', 'KAN-74 queued job root', $3, $3)`,
      [DELETION_JOB_FOLDER_ID, ORG_ID, USER_ADMIN],
    );
    await assetDb.query(
      `INSERT INTO folder_deletion_jobs
         (id, org_id, root_folder_id, root_path, requested_by, status, queued_at)
       VALUES ($1, $2, $3, 'root.kan74job', $4, 'queued', now())`,
      [QUEUED_DELETION_JOB_ID, ORG_ID, DELETION_JOB_FOLDER_ID, USER_ADMIN],
    );

    const result = await queryGraphQL<unknown>(
      `mutation($orgId: ID!, $folderId: ID!) {
        previewFolderDeletion(orgId: $orgId, folderId: $folderId) { id }
      }`,
      { orgId: ORG_ID, folderId: DELETION_JOB_FOLDER_ID },
    );

    expect(result.data).toBeNull();
    expectAssetError(result, {
      code: "FOLDER_DELETION_IN_PROGRESS",
      number: 3009,
      message: "Folder deletion is already in progress",
    });
  });

  test("forwards the Asset missing-deletion-job envelope", async () => {
    const result = await queryGraphQL<unknown>(
      `query($orgId: ID!, $id: ID!) {
        folderDeletionJob(orgId: $orgId, id: $id) { id }
      }`,
      { orgId: ORG_ID, id: MISSING_DELETION_JOB_ID },
    );

    expect(result.data).toBeNull();
    expectAssetError(result, {
      code: "DELETION_JOB_NOT_FOUND",
      number: 3010,
      message: "Folder deletion job not found",
    });
    expect(JSON.stringify(result.errors)).not.toContain(MISSING_DELETION_JOB_ID);
  });

  test("forwards the Asset non-cancellable-deletion-job envelope", async () => {
    await assetDb.query(
      `INSERT INTO folder_deletion_jobs
         (id, org_id, root_folder_id, root_path, requested_by, status, started_at)
       VALUES ($1, $2, $3, 'root', $4, 'running', now())`,
      [RUNNING_DELETION_JOB_ID, ORG_ID, ROOT_FOLDER_ID, USER_ADMIN],
    );

    const result = await queryGraphQL<unknown>(
      `mutation($orgId: ID!, $id: ID!) {
        cancelFolderDeletion(orgId: $orgId, id: $id) { id }
      }`,
      { orgId: ORG_ID, id: RUNNING_DELETION_JOB_ID },
    );

    expect(result.data).toBeNull();
    expectAssetError(result, {
      code: "DELETION_JOB_NOT_CANCELLABLE",
      number: 3011,
      message: "Folder deletion job cannot be cancelled or retried in its current state",
    });
  });

  test("forwards the Asset duplicate-external-identity envelope without creating a row", async () => {
    await assetDb.query(
      `INSERT INTO metadata_items
         (id, folder_id, title, labels, metadata_json, external_source, external_id, created_by, updated_by)
       VALUES ($1, $2, 'KAN-74 existing identity', ARRAY['kan74-e2e'], '{}'::jsonb, $3, $4, $5, $5)`,
      [IDENTITY_METADATA_ID, ROOT_FOLDER_ID, IDENTITY_EXTERNAL_SOURCE, IDENTITY_EXTERNAL_ID, USER_ADMIN],
    );

    const result = await queryGraphQL<unknown>(
      `mutation($orgId: ID!, $input: CreateMetadataInput!) {
        createMetadata(orgId: $orgId, input: $input) { id }
      }`,
      {
        orgId: ORG_ID,
        input: {
          folderId: ROOT_FOLDER_ID,
          title: "KAN-74 duplicate identity",
          externalSource: IDENTITY_EXTERNAL_SOURCE,
          externalId: IDENTITY_EXTERNAL_ID,
        },
      },
    );

    expect(result.data).toBeNull();
    expectAssetError(result, {
      code: "METADATA_IDENTITY_CONFLICT",
      number: 4002,
      message: "External identity already exists on an active item",
    });
    const persisted = await assetDb.query<{ count: number }>(
      "SELECT COUNT(*)::int AS count FROM metadata_items WHERE external_source = $1 AND external_id = $2 AND deleted_at IS NULL",
      [IDENTITY_EXTERNAL_SOURCE, IDENTITY_EXTERNAL_ID],
    );
    expect(persisted.rows[0].count).toBe(1);
  });

  test("forwards the Asset metadata-validation envelope without persisting an item", async () => {
    const result = await queryGraphQL<unknown>(
      `mutation($orgId: ID!, $input: CreateMetadataInput!) {
        createMetadata(orgId: $orgId, input: $input) { id }
      }`,
      { orgId: ORG_ID, input: { folderId: ROOT_FOLDER_ID, title: " " } },
    );

    expect(result.data).toBeNull();
    expectAssetError(result, {
      code: "METADATA_VALIDATION_ERROR",
      number: 4003,
      message: "Metadata field validation failed",
    });
  });

  test("forwards the Asset metadata-folder-deleted envelope without reviving the item", async () => {
    await assetDb.query(
      `INSERT INTO folders (id, org_id, path, name, created_by, updated_by)
       VALUES ($1, $2, 'root.kan74metadeleted', 'KAN-74 deleted metadata folder', $3, $3)`,
      [DELETED_METADATA_FOLDER_ID, ORG_ID, USER_ADMIN],
    );
    await assetDb.query(
      `INSERT INTO metadata_items
         (id, folder_id, title, labels, metadata_json, created_by, updated_by, deleted_at)
       VALUES ($1, $2, 'KAN-74 deleted metadata', ARRAY['kan74-e2e'], '{}'::jsonb, $3, $3, now())`,
      [DELETED_RESTORE_METADATA_ID, DELETED_METADATA_FOLDER_ID, USER_ADMIN],
    );
    await assetDb.query("UPDATE folders SET deleted_at = now() WHERE id = $1", [DELETED_METADATA_FOLDER_ID]);

    const result = await queryGraphQL<unknown>(
      `mutation($orgId: ID!, $id: ID!) {
        restoreMetadata(orgId: $orgId, id: $id) { id }
      }`,
      { orgId: ORG_ID, id: DELETED_RESTORE_METADATA_ID },
    );

    expect(result.data).toBeNull();
    expectAssetError(result, {
      code: "METADATA_FOLDER_DELETED",
      number: 4004,
      message: "Containing folder is deleted",
    });
    const persisted = await assetDb.query<{ count: number }>(
      "SELECT COUNT(*)::int AS count FROM metadata_items WHERE id = $1 AND deleted_at IS NOT NULL",
      [DELETED_RESTORE_METADATA_ID],
    );
    expect(persisted.rows[0].count).toBe(1);
  });

  test("forwards the Asset already-active-metadata envelope without changing the row", async () => {
    await assetDb.query(
      `INSERT INTO metadata_items
         (id, folder_id, title, labels, metadata_json, created_by, updated_by)
       VALUES ($1, $2, 'KAN-74 active metadata', ARRAY['kan74-e2e'], '{}'::jsonb, $3, $3)`,
      [ACTIVE_RESTORE_METADATA_ID, ROOT_FOLDER_ID, USER_ADMIN],
    );

    const result = await queryGraphQL<unknown>(
      `mutation($orgId: ID!, $id: ID!) {
        restoreMetadata(orgId: $orgId, id: $id) { id }
      }`,
      { orgId: ORG_ID, id: ACTIVE_RESTORE_METADATA_ID },
    );

    expect(result.data).toBeNull();
    expectAssetError(result, {
      code: "METADATA_NOT_DELETED",
      number: 4005,
      message: "Metadata item is already active",
    });
    const persisted = await assetDb.query<{ count: number }>(
      "SELECT COUNT(*)::int AS count FROM metadata_items WHERE id = $1 AND deleted_at IS NULL",
      [ACTIVE_RESTORE_METADATA_ID],
    );
    expect(persisted.rows[0].count).toBe(1);
  });
});
