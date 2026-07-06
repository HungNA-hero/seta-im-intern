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
const READ_ACTION_ID = "30000000-0000-0000-0000-000000000001";
const WRITE_ACTION_ID = "30000000-0000-0000-0000-000000000002";
const FOLDER_ID = "90000000-0000-0000-0000-000000000001";
const WRONG_FOLDER_ID = "90000000-0000-0000-0000-000000000002";
const BASE_FOLDER_NAME = "Target Folder";

interface GraphQLErrorResult {
  message?: string;
  extensions?: { code?: string };
}

interface GraphQLResult<T> {
  data?: T;
  errors?: GraphQLErrorResult[];
}

interface FolderResult {
  folder: { id: string; name: string };
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
    where: { resourceId: { in: [FOLDER_ID, WRONG_FOLDER_ID] } },
  });
  await prisma.organization.update({
    where: { id: ORG_ID },
    data: { olpEnabled: false },
  });
}

/** Recreates the Asset DB target with deterministic state for each test. */
async function resetTargetFolder(): Promise<void> {
  await assetDb.query("DELETE FROM folders WHERE id = $1", [FOLDER_ID]);
  await assetDb.query(
    `INSERT INTO folders (id, org_id, path, name, created_by, updated_by)
     VALUES ($1::uuid, $2::uuid, $3::ltree, $4, $5::uuid, $5::uuid)`,
    [
      FOLDER_ID,
      ORG_ID,
      FOLDER_ID.replace(/-/g, ""),
      BASE_FOLDER_NAME,
      USER_ADMIN,
    ],
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
  await assetDb.query("DELETE FROM folders WHERE id = $1", [FOLDER_ID]);
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

  test("PM-03 allows viewer read through RBAC ceiling plus an explicit grant", async () => {
    await prisma.objectPermission.create({
      data: {
        orgId: ORG_ID,
        resourceType: "folder",
        resourceId: FOLDER_ID,
        actionId: READ_ACTION_ID,
        granteeUserId: USER_VIEWER,
        grantedBy: USER_ADMIN,
      },
    });
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
    expect(fetchSpy).toHaveBeenCalledTimes(2);
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
    expect(fetchSpy).toHaveBeenCalledTimes(1);
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
});
