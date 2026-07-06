import { execSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
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
import { createCanDoMock } from "./helpers/canDoMock";
import { prisma } from "../db/prisma";
import { buildServer } from "../server";

const { mockCanDo, mockFilterAllowedResourceIds } = vi.hoisted(() => ({
  mockCanDo: vi.fn(),
  mockFilterAllowedResourceIds: vi.fn(),
}));

vi.mock("../db/queries/canDo", () =>
  createCanDoMock(mockCanDo, mockFilterAllowedResourceIds),
);

const ORG_ID = "00000000-0000-0000-0000-000000000010"; // Test Org
const ORG_ID_OTHER = "00000000-0000-0000-0000-000000000011"; // Other Org
const USER_ID = "00000000-0000-0000-0000-000000000001"; // Test User

interface GraphQLErrorResult {
  message?: string;
  extensions?: { code?: string };
}

describe("CLI Import Visibility Policy E2E", () => {
  let app: FastifyInstance;
  let pgClient: Client;
  let importedFolderId = "";
  let importedItemId = "";
  const tempPayloadPath = path.join(__dirname, "temp_payload.json");

  beforeAll(async () => {
    app = await buildServer();
    await app.ready();

    if (!process.env.ASSET_DB_URL) {
      throw new Error("ASSET_DB_URL is required");
    }
    const dbUrl = process.env.ASSET_DB_URL;
    pgClient = new Client({ connectionString: dbUrl });
    await pgClient.connect();

    // Ensure users and orgs exist in both access DB and asset DB shadow refs
    await prisma.organization.upsert({
      where: { id: ORG_ID },
      update: { name: "Test Org" },
      create: { id: ORG_ID, name: "Test Org", code: "TEST_ORG" },
    });
    await prisma.organization.upsert({
      where: { id: ORG_ID_OTHER },
      update: { name: "Other Org" },
      create: { id: ORG_ID_OTHER, name: "Other Org", code: "OTHER_ORG" },
    });

    await prisma.user.upsert({
      where: { id: USER_ID },
      update: { email: "user@test.com", displayName: "User" },
      create: { id: USER_ID, email: "user@test.com", displayName: "User" },
    });
    await prisma.organizationMember.upsert({
      where: { orgId_userId: { userId: USER_ID, orgId: ORG_ID } },
      update: {},
      create: { userId: USER_ID, orgId: ORG_ID },
    });
    await prisma.organizationMember.upsert({
      where: { orgId_userId: { userId: USER_ID, orgId: ORG_ID_OTHER } },
      update: {},
      create: { userId: USER_ID, orgId: ORG_ID_OTHER },
    });

    // Shadow refs
    await pgClient.query(
      "INSERT INTO user_ref (user_id) VALUES ($1) ON CONFLICT DO NOTHING",
      [USER_ID],
    );
    await pgClient.query(
      "INSERT INTO organization_ref (org_id) VALUES ($1) ON CONFLICT DO NOTHING",
      [ORG_ID],
    );
    await pgClient.query(
      "INSERT INTO organization_ref (org_id) VALUES ($1) ON CONFLICT DO NOTHING",
      [ORG_ID_OTHER],
    );

    // Create payload
    const dataset = {
      version: 1,
      external_source: "open_images_v7",
      folders: [{ key: "root", name: "Imported Root" }],
      metadata: [
        {
          folder_key: "root",
          external_id: "ext1",
          title: "Imported Item",
          metadata_json: {},
        },
      ],
    };
    fs.writeFileSync(tempPayloadPath, JSON.stringify(dataset));

    // Run Go CLI import using 'go run'
    try {
      execSync(
        `go run cmd/import-sample/main.go --file "${tempPayloadPath}" --org-id ${ORG_ID} --user-id ${USER_ID} --database-url "${dbUrl}"`,
        {
          cwd: path.join(__dirname, "../../../asset-core"), // asset-core root
          stdio: "inherit",
        },
      );
    } catch (e) {
      console.error("CLI execution failed", e);
      throw e;
    }

    const importedRows = await pgClient.query<{
      folder_id: string;
      item_id: string;
    }>(
      `SELECT f.id AS folder_id, m.id AS item_id
       FROM folders f
       JOIN metadata_items m ON m.folder_id = f.id
       WHERE f.org_id = $1
         AND m.external_source = 'open_images_v7'
         AND m.external_id = 'ext1'
         AND f.deleted_at IS NULL
         AND m.deleted_at IS NULL`,
      [ORG_ID],
    );
    if (importedRows.rows.length !== 1) {
      throw new Error(
        "CLI import did not create exactly one visible metadata item",
      );
    }
    importedFolderId = importedRows.rows[0].folder_id;
    importedItemId = importedRows.rows[0].item_id;
  });

  afterAll(async () => {
    await app.close();
    await pgClient.end();
    if (fs.existsSync(tempPayloadPath)) {
      fs.unlinkSync(tempPayloadPath);
    }
  });

  beforeEach(async () => {
    vi.resetAllMocks();
  });

  /** Executes GraphQL with the same identity headers used by production requests. */
  const queryGraphQL = async (
    query: string,
    variables: any,
    userAuth: string,
    orgAuth: string,
  ) => {
    return app.inject({
      method: "POST",
      url: "/graphql",
      headers: {
        "x-user-id": userAuth,
        "x-org-id": orgAuth,
      },
      payload: { query, variables },
    });
  };

  test("imported items are visible through SearchMetadataItems with read policy", async () => {
    mockFilterAllowedResourceIds.mockImplementation(
      async (_userId: string, _orgId: string, _action: string, _type: string, ids: string[]) =>
        new Set(ids),
    ); // authorized

    const query = `
      query {
        searchMetadata(orgId: "${ORG_ID}", input: { externalSource: "open_images_v7", limit: 10 }) {
          id
          title
        }
      }
    `;

    const res = await queryGraphQL(query, {}, USER_ID, ORG_ID);
    const body = res.json();

    expect(res.statusCode).toBe(200);
    expect(body.errors).toBeUndefined();
    expect(body.data.searchMetadata.length).toBeGreaterThanOrEqual(1);
    expect(
      body.data.searchMetadata.some((i: any) => i.title === "Imported Item"),
    ).toBe(true);

    expect(mockFilterAllowedResourceIds).toHaveBeenCalledWith(
      USER_ID,
      ORG_ID,
      "read",
      "metadata_item",
      expect.arrayContaining([importedItemId]),
    );
  });

  test("imported items are visible through metadata list with folder read policy", async () => {
    mockFilterAllowedResourceIds.mockImplementation(
      async (_userId: string, _orgId: string, _action: string, _type: string, ids: string[]) =>
        new Set(ids),
    );

    const res = await queryGraphQL(
      `query($orgId: ID!, $folderId: ID!) {
        metadataItems(orgId: $orgId, folderId: $folderId) { id title }
      }`,
      { orgId: ORG_ID, folderId: importedFolderId },
      USER_ID,
      ORG_ID,
    );
    const body = res.json();

    expect(body.errors).toBeUndefined();
    expect(body.data.metadataItems).toEqual([
      expect.objectContaining({ id: importedItemId, title: "Imported Item" }),
    ]);
    expect(mockFilterAllowedResourceIds).toHaveBeenCalledWith(
      USER_ID,
      ORG_ID,
      "read",
      "metadata_item",
      expect.arrayContaining([importedItemId]),
    );
  });

  test("imported item detail is visible with metadata read policy", async () => {
    mockCanDo.mockResolvedValue({ allowed: true, reason: null });

    const res = await queryGraphQL(
      `query($orgId: ID!, $id: ID!) {
        metadataItem(orgId: $orgId, id: $id) { id title externalSource externalId }
      }`,
      { orgId: ORG_ID, id: importedItemId },
      USER_ID,
      ORG_ID,
    );
    const body = res.json();

    expect(body.errors).toBeUndefined();
    expect(body.data.metadataItem).toMatchObject({
      id: importedItemId,
      title: "Imported Item",
      externalSource: "open_images_v7",
      externalId: "ext1",
    });
    expect(mockCanDo).toHaveBeenCalledWith(
      USER_ID,
      "read",
      "metadata_item",
      importedItemId,
      ORG_ID,
    );
  });

  test("denied requester cannot read imported item through detail", async () => {
    mockCanDo.mockResolvedValue({ allowed: false, reason: "denied" });

    const res = await queryGraphQL(
      `query($orgId: ID!, $id: ID!) {
        metadataItem(orgId: $orgId, id: $id) { id }
      }`,
      { orgId: ORG_ID, id: importedItemId },
      USER_ID,
      ORG_ID,
    );
    const body = res.json() as { errors?: GraphQLErrorResult[] };

    expect(body.errors?.[0]?.extensions?.code).toBe("FORBIDDEN");
  });

  test("denied requester gets an empty list for the imported item", async () => {
    mockFilterAllowedResourceIds.mockResolvedValue(new Set());

    const res = await queryGraphQL(
      `query($orgId: ID!, $folderId: ID!) {
        metadataItems(orgId: $orgId, folderId: $folderId) { id }
      }`,
      { orgId: ORG_ID, folderId: importedFolderId },
      USER_ID,
      ORG_ID,
    );
    const body = res.json();

    expect(body.errors).toBeUndefined();
    expect(body.data.metadataItems).toEqual([]);
  });

  test("imported items are omitted from search if policy returns false", async () => {
    mockFilterAllowedResourceIds.mockResolvedValue(new Set()); // denied

    const query = `
      query {
        searchMetadata(orgId: "${ORG_ID}", input: { externalSource: "open_images_v7", limit: 10 }) {
          id
          title
        }
      }
    `;

    const res = await queryGraphQL(query, {}, USER_ID, ORG_ID);
    const body = res.json();

    expect(res.statusCode).toBe(200);
    expect(body.errors).toBeUndefined();
    expect(body.data.searchMetadata).toHaveLength(0); // Items are omitted, no FORBIDDEN thrown for search
  });

  test("imported items are not visible in cross-org search", async () => {
    mockFilterAllowedResourceIds.mockImplementation(
      async (_userId: string, _orgId: string, _action: string, _type: string, ids: string[]) =>
        new Set(ids),
    );

    const query = `
      query {
        searchMetadata(orgId: "${ORG_ID_OTHER}", input: { externalSource: "open_images_v7", limit: 10 }) {
          id
          title
        }
      }
    `;

    const res = await queryGraphQL(query, {}, USER_ID, ORG_ID_OTHER);
    const body = res.json();

    expect(res.statusCode).toBe(200);
    expect(body.errors).toBeUndefined();
    expect(body.data.searchMetadata).toHaveLength(0); // Zero results due to tenant isolation
  });
});
