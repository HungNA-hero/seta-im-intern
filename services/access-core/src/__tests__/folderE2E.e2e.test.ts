import { randomUUID } from "node:crypto";
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
vi.mock("../authz/decision", () => ({ canDo: mockCanDo }));

const ORG_ID = "00000000-0000-0000-0000-000000000010";
const USER_ID = "00000000-0000-0000-0000-000000000001";

interface GraphQLErrorResult {
  message?: string;
  extensions?: { code?: string };
}

interface GraphQLResult<T> {
  data?: T;
  errors?: GraphQLErrorResult[];
}

interface FolderSummary {
  id: string;
  path: string;
  name: string;
}

interface FolderRow {
  path: string;
  updated_by: string | null;
  deleted_at: Date | null;
}

let app: FastifyInstance;
let assetDb: Client;

/** Executes GraphQL through Fastify/Yoga with production identity headers. */
async function queryGraphQL<T>(
  query: string,
  variables: Record<string, unknown>,
): Promise<GraphQLResult<T>> {
  const response = await app.inject({
    method: "POST",
    url: "/graphql",
    headers: {
      "Content-Type": "application/json",
      "x-user-id": USER_ID,
      "x-org-id": ORG_ID,
    },
    payload: { query, variables },
  });

  return response.json() as GraphQLResult<T>;
}

/** Creates a uniquely named folder and returns its persisted identity and path. */
async function createFolder(
  label: string,
  parentPath?: string,
  nameOverride?: string,
): Promise<FolderSummary> {
  const name =
    nameOverride ?? `KAN36 E2E: ${label} ${randomUUID().slice(0, 8)}`;
  const result = await queryGraphQL<{ createFolder: FolderSummary }>(
    `mutation($orgId: ID!, $name: String!, $parentPath: String) {
      createFolder(orgId: $orgId, name: $name, parentPath: $parentPath) {
        id
        path
        name
      }
    }`,
    { orgId: ORG_ID, name, parentPath },
  );

  expect(result.errors).toBeUndefined();
  if (!result.data) throw new Error(`Failed to create ${label} fixture`);
  return result.data.createFolder;
}

/** Moves a folder through the public GraphQL mutation. */
async function moveFolder(
  id: string,
  destinationParentId: string | null,
): Promise<GraphQLResult<{ moveFolder: FolderSummary }>> {
  return queryGraphQL<{ moveFolder: FolderSummary }>(
    `mutation($orgId: ID!, $id: ID!, $destinationParentId: ID) {
      moveFolder(orgId: $orgId, id: $id, destinationParentId: $destinationParentId) {
        id
        path
        name
      }
    }`,
    { orgId: ORG_ID, id, destinationParentId },
  );
}

/** Deletes a folder through the public GraphQL mutation. */
async function deleteFolder(
  id: string,
): Promise<GraphQLResult<{ deleteFolder: boolean }>> {
  return queryGraphQL<{ deleteFolder: boolean }>(
    `mutation($orgId: ID!, $id: ID!) {
      deleteFolder(orgId: $orgId, id: $id)
    }`,
    { orgId: ORG_ID, id },
  );
}

/** Reads mutation-sensitive folder fields directly from Asset PostgreSQL. */
async function readFolder(id: string): Promise<FolderRow> {
  const result = await assetDb.query<FolderRow>(
    "SELECT path::text, updated_by::text, deleted_at FROM folders WHERE id = $1",
    [id],
  );
  if (result.rowCount !== 1) throw new Error(`Missing folder fixture ${id}`);
  return result.rows[0];
}

function firstErrorCode(result: GraphQLResult<unknown>): string | undefined {
  return result.errors?.[0]?.extensions?.code;
}

beforeAll(async () => {
  assetDb = new Client({ connectionString: process.env.ASSET_DB_URL });
  await assetDb.connect();
  app = await buildServer();
});

afterAll(async () => {
  await app.close();
  await assetDb.end();
  await prisma.$disconnect();
});

beforeEach(() => {
  vi.restoreAllMocks();
  mockCanDo.mockReset();
  mockCanDo.mockResolvedValue({ allowed: true, reason: null });
});

describe("KAN-36 folder GraphQL to PostgreSQL E2E", () => {
  test("moves a complete subtree and preserves metadata ownership", async () => {
    const archive = await createFolder("Archive");
    const animals = await createFolder("Animals");
    const dogs = await createFolder("Dogs", animals.path);
    const metadataId = randomUUID();
    await assetDb.query(
      "INSERT INTO metadata_items (id, folder_id, title, created_by) VALUES ($1, $2, $3, $4)",
      [metadataId, dogs.id, `KAN36 E2E: metadata ${randomUUID()}`, USER_ID],
    );

    const result = await moveFolder(animals.id, archive.id);
    expect(result.errors).toBeUndefined();

    const expectedAnimalsPath = `${archive.path}.${animals.id.replaceAll("-", "")}`;
    const expectedDogsPath = `${expectedAnimalsPath}.${dogs.id.replaceAll("-", "")}`;
    // A pg Client owns one connection, so keep direct verification queries sequential.
    const animalsRow = await readFolder(animals.id);
    const dogsRow = await readFolder(dogs.id);
    const metadataRow = await assetDb.query<{ folder_id: string }>(
      "SELECT folder_id::text FROM metadata_items WHERE id = $1",
      [metadataId],
    );

    expect(result.data?.moveFolder.path).toBe(expectedAnimalsPath);
    expect(animalsRow.path).toBe(expectedAnimalsPath);
    expect(dogsRow.path).toBe(expectedDogsPath);
    expect(animalsRow.updated_by).toBe(USER_ID);
    expect(dogsRow.updated_by).toBe(USER_ID);
    expect(metadataRow.rows[0].folder_id).toBe(dogs.id);
  });

  test("rejects a cycle and leaves the subtree unchanged", async () => {
    const parent = await createFolder("Cycle parent");
    const child = await createFolder("Cycle child", parent.path);
    const beforeParent = await readFolder(parent.id);
    const beforeChild = await readFolder(child.id);

    const result = await moveFolder(parent.id, child.id);
    expect(firstErrorCode(result)).toBe("FOLDER_CYCLE_DETECTED");
    expect(await readFolder(parent.id)).toEqual(beforeParent);
    expect(await readFolder(child.id)).toEqual(beforeChild);
  });

  test("rejects a sibling-name conflict and leaves the source unchanged", async () => {
    const destination = await createFolder("Conflict destination");
    const sharedName = `KAN36 E2E: duplicate ${randomUUID().slice(0, 8)}`;
    await createFolder("Existing sibling", destination.path, sharedName);
    const source = await createFolder("Conflict source", undefined, sharedName);
    const beforeSource = await readFolder(source.id);

    const result = await moveFolder(source.id, destination.id);
    expect(firstErrorCode(result)).toBe("FOLDER_NAME_CONFLICT");
    expect(await readFolder(source.id)).toEqual(beforeSource);
  });

  test("rejects deletion of a non-empty folder without changing the database", async () => {
    const parent = await createFolder("Non-empty parent");
    await createFolder("Non-empty child", parent.path);
    const beforeParent = await readFolder(parent.id);

    const result = await deleteFolder(parent.id);
    expect(firstErrorCode(result)).toBe("FOLDER_NOT_EMPTY");
    expect(await readFolder(parent.id)).toEqual(beforeParent);
  });

  test("fails closed before Go when policy denies move and delete", async () => {
    const source = await createFolder("Policy denied source");
    const beforeSource = await readFolder(source.id);
    const fetchSpy = vi.spyOn(globalThis, "fetch");
    mockCanDo.mockResolvedValue({ allowed: false, reason: "denied by policy" });

    const moveResult = await moveFolder(source.id, null);
    const deleteResult = await deleteFolder(source.id);

    expect(firstErrorCode(moveResult)).toBe("FORBIDDEN");
    expect(firstErrorCode(deleteResult)).toBe("FORBIDDEN");
    expect(fetchSpy).not.toHaveBeenCalled();
    expect(await readFolder(source.id)).toEqual(beforeSource);
  });

  test("hard-deletes an empty folder", async () => {
    const emptyFolder = await createFolder("Empty folder");
    const result = await deleteFolder(emptyFolder.id);
    const persisted = await assetDb.query<{ count: number }>(
      "SELECT COUNT(*)::int AS count FROM folders WHERE id = $1",
      [emptyFolder.id],
    );

    expect(result.errors).toBeUndefined();
    expect(result.data?.deleteFolder).toBe(true);
    expect(persisted.rows[0].count).toBe(0);
  });
});
