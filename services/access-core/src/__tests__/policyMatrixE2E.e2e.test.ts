import { Client } from "pg";
import type { FastifyInstance } from "fastify";
import {
  afterAll,
  afterEach,
  beforeAll,
  beforeEach,
  describe,
  expect,
  test,
  vi,
} from "vitest";
import { prisma } from "../db/prisma";
import { buildServer } from "../server";

const ORG_ID = "00000000-0000-0000-0000-000000000010";
const OTHER_ORG_ID = "00000000-0000-0000-0000-000000000099";
const USER_ADMIN = "00000000-0000-0000-0000-000000000001";
const USER_VIEWER = "00000000-0000-0000-0000-000000000002";
const MISSING_MEMBERSHIP_USER = "00000000-0000-0000-0000-000000000003";
const WRITE_ACTION_ID = "30000000-0000-0000-0000-000000000002";
const MANAGE_ACTION_ID = "30000000-0000-0000-0000-000000000004";
const ANCESTOR_FOLDER_ID = "90000000-0000-0000-0000-000000000010";
const FOLDER_ID = "90000000-0000-0000-0000-000000000001";
const WRONG_FOLDER_ID = "90000000-0000-0000-0000-000000000002";
const METADATA_ID = "91000000-0000-0000-0000-000000000001";
const BASE_FOLDER_NAME = "Target Folder";
const BASE_METADATA_TITLE = "Target Metadata";

interface GraphQLErrorResult {
  message?: string;
  extensions?: { code?: string };
}

interface GraphQLResult<T> {
  data?: T;
  errors?: GraphQLErrorResult[];
}

interface FolderResult {
  folder: { id: string; name: string } | null;
}

interface UpdateFolderResult {
  updateFolder: { id: string; name: string };
}

interface GrantResult {
  grantObjectPermission: { id: string };
}

interface RevokeResult {
  revokeObjectPermission: boolean;
}

interface BooleanMutationResult {
  deleteFolder?: boolean;
  deleteMetadata?: boolean;
}

interface MetadataResult {
  metadataItem: { id: string; title: string } | null;
}

interface UpdateMetadataResult {
  updateMetadata: { id: string; title: string };
}

let app: FastifyInstance;
let assetDb: Client;

/** Executes GraphQL with production identity headers and the real policy stack. */
async function queryGraphQL<T>(
  query: string,
  variables: Record<string, unknown>,
  userId: string,
  orgId: string = ORG_ID,
): Promise<GraphQLResult<T>> {
  const response = await app.inject({
    method: "POST",
    url: "/graphql",
    headers: {
      "Content-Type": "application/json",
      "x-user-id": userId,
      "x-org-id": orgId,
    },
    payload: { query, variables },
  });
  return response.json() as GraphQLResult<T>;
}

/** Asserts a safe policy denial with the expected decision reason. */
function expectForbidden(
  result: GraphQLResult<unknown>,
  expectedReason: string,
): void {
  expect(result.errors?.[0]).toMatchObject({
    message: expectedReason,
    extensions: { code: "FORBIDDEN" },
  });
  expect(JSON.stringify(result.errors)).not.toMatch(
    /prisma|postgres|database|sqlstate/i,
  );
}

/** Returns the persisted folder name used to prove mutation or non-mutation. */
async function readFolderName(): Promise<string> {
  const result = await assetDb.query<{ name: string }>(
    "SELECT name FROM folders WHERE id = $1",
    [FOLDER_ID],
  );
  expect(result.rows).toHaveLength(1);
  return result.rows[0].name;
}

/** Calls the protected folder mutation used by every policy matrix write case. */
async function updateFolder(
  userId: string,
  name: string,
): Promise<GraphQLResult<UpdateFolderResult>> {
  return queryGraphQL<UpdateFolderResult>(
    `mutation($orgId: ID!, $id: ID!, $name: String!) {
       updateFolder(orgId: $orgId, id: $id, name: $name) { id name }
     }`,
    { orgId: ORG_ID, id: FOLDER_ID, name },
    userId,
  );
}

/** Calls the protected metadata mutation used by the creator no-bypass case. */
async function updateMetadata(
  userId: string,
  title: string,
): Promise<GraphQLResult<UpdateMetadataResult>> {
  return queryGraphQL<UpdateMetadataResult>(
    `mutation($orgId: ID!, $id: ID!, $input: UpdateMetadataInput!) {
       updateMetadata(orgId: $orgId, id: $id, input: $input) { id title }
     }`,
    { orgId: ORG_ID, id: METADATA_ID, input: { title } },
    userId,
  );
}

/** Calls a public grant mutation for an arbitrary folder target. */
async function grantFolderPermission(
  userId: string,
  resourceId: string,
  action: "read" | "write" | "delete" | "manage_permissions",
  granteeUserId: string,
): Promise<GraphQLResult<GrantResult>> {
  return queryGraphQL<GrantResult>(
    `mutation($orgId: ID!, $resourceId: ID!, $action: PermissionAction!, $granteeUserId: ID!) {
       grantObjectPermission(
         orgId: $orgId
         resourceType: folder
         resourceId: $resourceId
         action: $action
         granteeUserId: $granteeUserId
       ) { id }
     }`,
    { orgId: ORG_ID, resourceId, action, granteeUserId },
    userId,
  );
}

/** Grants viewer write access through the public permission mutation. */
async function grantViewerWrite(): Promise<string> {
  const result = await queryGraphQL<GrantResult>(
    `mutation($orgId: ID!, $resourceType: ResourceType!, $resourceId: ID!, $action: PermissionAction!, $granteeUserId: ID!) {
       grantObjectPermission(
         orgId: $orgId
         resourceType: $resourceType
         resourceId: $resourceId
         action: $action
         granteeUserId: $granteeUserId
       ) { id }
     }`,
    {
      orgId: ORG_ID,
      resourceType: "folder",
      resourceId: FOLDER_ID,
      action: "write",
      granteeUserId: USER_VIEWER,
    },
    USER_ADMIN,
  );
  expect(result.errors).toBeUndefined();
  expect(result.data?.grantObjectPermission.id).toBeTruthy();
  return result.data!.grantObjectPermission.id;
}

/** Revokes a direct grant through the public permission mutation. */
async function revokePermission(id: string): Promise<void> {
  const result = await queryGraphQL<RevokeResult>(
    `mutation($id: ID!) { revokeObjectPermission(id: $id) }`,
    { id },
    USER_ADMIN,
  );
  expect(result.errors).toBeUndefined();
  expect(result.data?.revokeObjectPermission).toBe(true);
}

/** Removes direct-grant fixtures and restores the organization policy mode. */
async function resetAccessFixtures(): Promise<void> {
  await prisma.objectPermission.deleteMany({
    where: {
      resourceId: {
        in: [ANCESTOR_FOLDER_ID, FOLDER_ID, WRONG_FOLDER_ID, METADATA_ID],
      },
    },
  });
  await prisma.organization.update({
    where: { id: ORG_ID },
    data: { olpEnabled: false },
  });
}

/** Recreates the Asset DB target with deterministic state for each test. */
async function resetTargetFolder(): Promise<void> {
  await assetDb.query("DELETE FROM metadata_items WHERE id = $1", [
    METADATA_ID,
  ]);
  await assetDb.query("DELETE FROM folders WHERE id = ANY($1::uuid[])", [
    [FOLDER_ID, ANCESTOR_FOLDER_ID],
  ]);
  await assetDb.query(
    `INSERT INTO folders (id, org_id, path, name, created_by, updated_by)
     VALUES
       ($1::uuid, $3::uuid, $4::ltree, 'Policy Ancestor', $6::uuid, $6::uuid),
       ($2::uuid, $3::uuid, $5::ltree, $7, $6::uuid, $6::uuid)`,
    [
      ANCESTOR_FOLDER_ID,
      FOLDER_ID,
      ORG_ID,
      ANCESTOR_FOLDER_ID.replace(/-/g, ""),
      `${ANCESTOR_FOLDER_ID.replace(/-/g, "")}.${FOLDER_ID.replace(/-/g, "")}`,
      USER_ADMIN,
      BASE_FOLDER_NAME,
    ],
  );
}

/** Creates the deterministic metadata target only for cases that need it. */
async function createTargetMetadata(): Promise<void> {
  await assetDb.query(
    `INSERT INTO metadata_items (id, folder_id, title, created_by, updated_by)
     VALUES ($1::uuid, $2::uuid, $3, $4::uuid, $4::uuid)`,
    [METADATA_ID, FOLDER_ID, BASE_METADATA_TITLE, USER_ADMIN],
  );
}

beforeAll(async () => {
  assetDb = new Client({
    connectionString:
      process.env.ASSET_DB_URL ??
      "postgresql://asset_user:asset_password@127.0.0.1:5433/asset_db",
  });
  await assetDb.connect();
  app = await buildServer();
  await app.ready();

  await prisma.user.upsert({
    where: { id: MISSING_MEMBERSHIP_USER },
    update: {},
    create: {
      id: MISSING_MEMBERSHIP_USER,
      email: "missing@seta.com",
      displayName: "Missing Membership",
    },
  });
  await prisma.organization.upsert({
    where: { id: OTHER_ORG_ID },
    update: {},
    create: {
      id: OTHER_ORG_ID,
      code: "other_org_pm",
      name: "Other Org PM",
    },
  });
});

beforeEach(async () => {
  await resetAccessFixtures();
  await resetTargetFolder();
});

afterEach(async () => {
  vi.restoreAllMocks();
  await resetAccessFixtures();
  await resetTargetFolder();
});

afterAll(async () => {
  if (app) await app.close();
  await resetAccessFixtures();
  await assetDb.query("DELETE FROM metadata_items WHERE id = $1", [
    METADATA_ID,
  ]);
  await assetDb.query("DELETE FROM folders WHERE id = ANY($1::uuid[])", [
    [FOLDER_ID, ANCESTOR_FOLDER_ID],
  ]);
  await assetDb.end();
  await prisma.user.deleteMany({ where: { id: MISSING_MEMBERSHIP_USER } });
  await prisma.organization.deleteMany({ where: { id: OTHER_ORG_ID } });
  await prisma.$disconnect();
});

describe("KAN-41 Policy E2E Integration Matrix", () => {
  test("PM-01 denies a non-member before the Asset Core mutation", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch");
    const result = await updateFolder(MISSING_MEMBERSHIP_USER, "New Name");

    expectForbidden(result, "Forbidden: not a member of this organization");
    expect(fetchSpy).not.toHaveBeenCalled();
    expect(await readFolderName()).toBe(BASE_FOLDER_NAME);
  });

  test("PM-02 allows org admin writes with OLP disabled and enabled", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch");
    const disabledResult = await updateFolder(USER_ADMIN, "Admin Edit 1");

    expect(disabledResult.errors).toBeUndefined();
    expect(fetchSpy).toHaveBeenCalledTimes(1);
    expect(await readFolderName()).toBe("Admin Edit 1");

    await prisma.organization.update({
      where: { id: ORG_ID },
      data: { olpEnabled: true },
    });
    fetchSpy.mockClear();

    const enabledResult = await updateFolder(USER_ADMIN, "Admin Edit 2");
    expect(enabledResult.errors).toBeUndefined();
    expect(fetchSpy).toHaveBeenCalledTimes(1);
    expect(await readFolderName()).toBe("Admin Edit 2");
  });

  test("PM-03 allows viewer read through the RBAC ceiling", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch");
    const result = await queryGraphQL<FolderResult>(
      `query($orgId: ID!, $id: ID!) {
         folder(orgId: $orgId, id: $id) { id name }
       }`,
      { orgId: ORG_ID, id: FOLDER_ID },
      USER_VIEWER,
    );

    expect(result.errors).toBeUndefined();
    expect(result.data?.folder).toMatchObject({
      id: FOLDER_ID,
      name: BASE_FOLDER_NAME,
    });
    expect(fetchSpy).toHaveBeenCalledTimes(1);
  });

  test("PM-04 ignores a direct grant and denies viewer write while OLP is disabled", async () => {
    await prisma.objectPermission.create({
      data: {
        orgId: ORG_ID,
        resourceType: "folder",
        resourceId: FOLDER_ID,
        actionId: WRITE_ACTION_ID,
        granteeUserId: USER_VIEWER,
        grantedBy: USER_ADMIN,
      },
    });
    const fetchSpy = vi.spyOn(globalThis, "fetch");

    const result = await updateFolder(USER_VIEWER, "Viewer Edit");
    expectForbidden(result, "no RBAC ceiling");
    expect(fetchSpy).not.toHaveBeenCalled();
    expect(await readFolderName()).toBe(BASE_FOLDER_NAME);
  });

  test("PM-05 denies viewer write when OLP is enabled without a matching grant", async () => {
    await prisma.organization.update({
      where: { id: ORG_ID },
      data: { olpEnabled: true },
    });
    const fetchSpy = vi.spyOn(globalThis, "fetch");

    const result = await updateFolder(USER_VIEWER, "Viewer Edit");
    expectForbidden(result, "no object permission");
    expect(fetchSpy).toHaveBeenCalledTimes(1);
    expect(await readFolderName()).toBe(BASE_FOLDER_NAME);
  });

  test("PM-06 allows one viewer mutation with an exact OLP grant", async () => {
    await prisma.organization.update({
      where: { id: ORG_ID },
      data: { olpEnabled: true },
    });
    await prisma.objectPermission.create({
      data: {
        orgId: ORG_ID,
        resourceType: "folder",
        resourceId: FOLDER_ID,
        actionId: WRITE_ACTION_ID,
        granteeUserId: USER_VIEWER,
        grantedBy: USER_ADMIN,
      },
    });
    const fetchSpy = vi.spyOn(globalThis, "fetch");

    const result = await updateFolder(USER_VIEWER, "Viewer Edit");
    expect(result.errors).toBeUndefined();
    expect(fetchSpy).toHaveBeenCalledTimes(2);
    expect(await readFolderName()).toBe("Viewer Edit");
  });

  test("PM-07 changes viewer write from deny to allow after a public grant", async () => {
    await prisma.organization.update({
      where: { id: ORG_ID },
      data: { olpEnabled: true },
    });
    const fetchSpy = vi.spyOn(globalThis, "fetch");

    const denied = await updateFolder(USER_VIEWER, "Pre-grant Edit");
    expectForbidden(denied, "no object permission");
    expect(fetchSpy).toHaveBeenCalledTimes(1);
    expect(await readFolderName()).toBe(BASE_FOLDER_NAME);

    const permissionId = await grantViewerWrite();
    const permission = await prisma.objectPermission.findUnique({
      where: { id: permissionId },
      include: { action: true },
    });
    expect(permission).toMatchObject({
      orgId: ORG_ID,
      resourceType: "folder",
      resourceId: FOLDER_ID,
      granteeUserId: USER_VIEWER,
      granteeRoleId: null,
      grantedBy: USER_ADMIN,
      action: { code: "write" },
    });

    fetchSpy.mockClear();
    const allowed = await updateFolder(USER_VIEWER, "Post-grant Edit");
    expect(allowed.errors).toBeUndefined();
    expect(fetchSpy).toHaveBeenCalledTimes(2);
    expect(await readFolderName()).toBe("Post-grant Edit");
  });

  test("PM-08 changes viewer write from allow to deny after a public revoke", async () => {
    await prisma.organization.update({
      where: { id: ORG_ID },
      data: { olpEnabled: true },
    });
    const permissionId = await grantViewerWrite();
    const fetchSpy = vi.spyOn(globalThis, "fetch");

    const allowed = await updateFolder(USER_VIEWER, "Pre-revoke Edit");
    expect(allowed.errors).toBeUndefined();
    expect(fetchSpy).toHaveBeenCalledTimes(2);
    expect(await readFolderName()).toBe("Pre-revoke Edit");

    await revokePermission(permissionId);
    expect(
      await prisma.objectPermission.findUnique({ where: { id: permissionId } }),
    ).toBeNull();

    fetchSpy.mockClear();
    const denied = await updateFolder(USER_VIEWER, "Post-revoke Edit");
    expectForbidden(denied, "no object permission");
    expect(fetchSpy).toHaveBeenCalledTimes(1);
    expect(await readFolderName()).toBe("Pre-revoke Edit");
  });

  test("PM-09 ignores grants from the wrong resource and wrong organization", async () => {
    await prisma.organization.update({
      where: { id: ORG_ID },
      data: { olpEnabled: true },
    });
    await prisma.objectPermission.createMany({
      data: [
        {
          orgId: ORG_ID,
          resourceType: "folder",
          resourceId: WRONG_FOLDER_ID,
          actionId: WRITE_ACTION_ID,
          granteeUserId: USER_VIEWER,
          grantedBy: USER_ADMIN,
        },
        {
          orgId: OTHER_ORG_ID,
          resourceType: "folder",
          resourceId: FOLDER_ID,
          actionId: WRITE_ACTION_ID,
          granteeUserId: USER_VIEWER,
          grantedBy: USER_ADMIN,
        },
      ],
    });
    const fetchSpy = vi.spyOn(globalThis, "fetch");

    const result = await updateFolder(USER_VIEWER, "Viewer Edit");
    expectForbidden(result, "no object permission");
    expect(fetchSpy).toHaveBeenCalledTimes(1);
    expect(await readFolderName()).toBe(BASE_FOLDER_NAME);
  });

  test("PM-10 creator status does not bypass RBAC or OLP policy", async () => {
    await createTargetMetadata();
    await assetDb.query("UPDATE folders SET created_by = $1 WHERE id = $2", [
      USER_VIEWER,
      FOLDER_ID,
    ]);
    await assetDb.query(
      "UPDATE metadata_items SET created_by = $1 WHERE id = $2",
      [USER_VIEWER, METADATA_ID],
    );
    const fetchSpy = vi.spyOn(globalThis, "fetch");

    const rbacFolder = await updateFolder(USER_VIEWER, "Creator Folder Edit");
    expectForbidden(rbacFolder, "no RBAC ceiling");
    expect(fetchSpy).not.toHaveBeenCalled();

    const rbacMetadata = await updateMetadata(
      USER_VIEWER,
      "Creator Metadata Edit",
    );
    expectForbidden(rbacMetadata, "no RBAC ceiling");
    expect(fetchSpy).not.toHaveBeenCalled();

    await prisma.organization.update({
      where: { id: ORG_ID },
      data: { olpEnabled: true },
    });
    fetchSpy.mockClear();

    const olpFolder = await updateFolder(USER_VIEWER, "Creator Folder Edit");
    expectForbidden(olpFolder, "no object permission");
    expect(fetchSpy).toHaveBeenCalledTimes(1);
    expect(await readFolderName()).toBe(BASE_FOLDER_NAME);

    fetchSpy.mockClear();
    const olpMetadata = await updateMetadata(
      USER_VIEWER,
      "Creator Metadata Edit",
    );
    expectForbidden(olpMetadata, "no object permission");
    expect(fetchSpy).toHaveBeenCalledTimes(2);
    const persisted = await assetDb.query<{ title: string }>(
      "SELECT title FROM metadata_items WHERE id = $1",
      [METADATA_ID],
    );
    expect(persisted.rows[0].title).toBe(BASE_METADATA_TITLE);
  });

  test("PM-11 allows a descendant folder write through an ancestor grant", async () => {
    await prisma.organization.update({
      where: { id: ORG_ID },
      data: { olpEnabled: true },
    });
    await prisma.objectPermission.create({
      data: {
        orgId: ORG_ID,
        resourceType: "folder",
        resourceId: ANCESTOR_FOLDER_ID,
        actionId: WRITE_ACTION_ID,
        granteeUserId: USER_VIEWER,
        grantedBy: USER_ADMIN,
      },
    });
    const fetchSpy = vi.spyOn(globalThis, "fetch");

    const result = await updateFolder(USER_VIEWER, "Inherited Edit");

    expect(result.errors).toBeUndefined();
    expect(result.data?.updateFolder.name).toBe("Inherited Edit");
    expect(fetchSpy).toHaveBeenCalledTimes(2);
    expect(await readFolderName()).toBe("Inherited Edit");
  });

  test("PM-12 soft delete preserves grants and hides deleted resources", async () => {
    await createTargetMetadata();
    await prisma.objectPermission.createMany({
      data: [
        {
          orgId: ORG_ID,
          resourceType: "folder",
          resourceId: FOLDER_ID,
          actionId: WRITE_ACTION_ID,
          granteeUserId: USER_VIEWER,
          grantedBy: USER_ADMIN,
        },
        {
          orgId: ORG_ID,
          resourceType: "metadata_item",
          resourceId: METADATA_ID,
          actionId: WRITE_ACTION_ID,
          granteeUserId: USER_VIEWER,
          grantedBy: USER_ADMIN,
        },
      ],
    });
    const permissionCountBefore = await prisma.objectPermission.count({
      where: { resourceId: { in: [FOLDER_ID, METADATA_ID] } },
    });
    const fetchSpy = vi.spyOn(globalThis, "fetch");

    const deleteMetadataResult = await queryGraphQL<BooleanMutationResult>(
      `mutation($orgId: ID!, $id: ID!) {
         deleteMetadata(orgId: $orgId, id: $id)
       }`,
      { orgId: ORG_ID, id: METADATA_ID },
      USER_ADMIN,
    );
    expect(deleteMetadataResult.errors).toBeUndefined();
    expect(deleteMetadataResult.data?.deleteMetadata).toBe(true);

    const deleteFolderResult = await queryGraphQL<BooleanMutationResult>(
      `mutation($orgId: ID!, $id: ID!) {
         deleteFolder(orgId: $orgId, id: $id)
       }`,
      { orgId: ORG_ID, id: FOLDER_ID },
      USER_ADMIN,
    );
    expect(deleteFolderResult.errors).toBeUndefined();
    expect(deleteFolderResult.data?.deleteFolder).toBe(true);
    expect(fetchSpy).toHaveBeenCalledTimes(2);

    const deletedFolder = await assetDb.query<{ deleted_at: Date | null }>(
      "SELECT deleted_at FROM folders WHERE id = $1",
      [FOLDER_ID],
    );
    const deletedMetadata = await assetDb.query<{ deleted_at: Date | null }>(
      "SELECT deleted_at FROM metadata_items WHERE id = $1",
      [METADATA_ID],
    );
    expect(deletedFolder.rows[0].deleted_at).not.toBeNull();
    expect(deletedMetadata.rows[0].deleted_at).not.toBeNull();
    expect(
      await prisma.objectPermission.count({
        where: { resourceId: { in: [FOLDER_ID, METADATA_ID] } },
      }),
    ).toBe(permissionCountBefore);

    const folderRead = await queryGraphQL<FolderResult>(
      `query($orgId: ID!, $id: ID!) {
         folder(orgId: $orgId, id: $id) { id name }
       }`,
      { orgId: ORG_ID, id: FOLDER_ID },
      USER_ADMIN,
    );
    expect(folderRead.errors).toBeUndefined();
    expect(folderRead.data?.folder).toBeNull();

    const metadataRead = await queryGraphQL<MetadataResult>(
      `query($orgId: ID!, $id: ID!) {
         metadataItem(orgId: $orgId, id: $id) { id title }
       }`,
      { orgId: ORG_ID, id: METADATA_ID },
      USER_ADMIN,
    );
    expect(metadataRead.errors).toBeUndefined();
    expect(metadataRead.data?.metadataItem).toBeNull();
  });

  test("PM-13 manage_permissions never inherits from an ancestor", async () => {
    await prisma.organization.update({
      where: { id: ORG_ID },
      data: { olpEnabled: true },
    });
    await prisma.objectPermission.create({
      data: {
        orgId: ORG_ID,
        resourceType: "folder",
        resourceId: ANCESTOR_FOLDER_ID,
        actionId: MANAGE_ACTION_ID,
        granteeUserId: USER_VIEWER,
        grantedBy: USER_ADMIN,
      },
    });
    const permissionCountBefore = await prisma.objectPermission.count();
    const fetchSpy = vi.spyOn(globalThis, "fetch");

    const result = await grantFolderPermission(
      USER_VIEWER,
      FOLDER_ID,
      "read",
      USER_ADMIN,
    );

    expectForbidden(result, "no object permission");
    expect(fetchSpy).not.toHaveBeenCalled();
    expect(await prisma.objectPermission.count()).toBe(permissionCountBefore);
  });
});
