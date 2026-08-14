import { Client } from "pg";
import type { FastifyInstance } from "fastify";
import { afterAll, afterEach, beforeAll, describe, expect, test } from "vitest";
import { prisma } from "../db/prisma";
import { buildServer } from "../server";

const ORG_ID = "00000000-0000-0000-0000-000000000010";
const USER_ADMIN = "00000000-0000-0000-0000-000000000001";
const UNIT_A_ID = "70000000-0000-0000-0000-000000000083";
const UNIT_B_ID = "70000000-0000-0000-0000-000000000084";
const FOLDER_A_ID = "71000000-0000-0000-0000-000000000083";
const FOLDER_B_ID = "71000000-0000-0000-0000-000000000084";
const ROOT_PATH = "root.kan83lifecyclea";
const CHILD_PATH = "root.kan83lifecyclea.kan83lifecycleb";
const TRACE_ID = "83a83a83a83a83a83a83a83a83a83a83";
const lifecycleWorkerEnabled = process.env.E2E_LIFECYCLE_WORKER === "true";

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

async function queryGraphQL<T>(query: string, variables: Record<string, unknown>): Promise<GraphQLResult<T>> {
  const response = await app.inject({
    method: "POST",
    url: "/graphql",
    headers: {
      "Content-Type": "application/json",
      "x-user-id": USER_ADMIN,
      "x-org-id": ORG_ID,
      traceparent: `00-${TRACE_ID}-0123456789abcdef-01`,
      "x-request-id": "kan-83-lifecycle-restore-e2e",
    },
    payload: { query, variables },
  });
  return response.json() as GraphQLResult<T>;
}

async function queueParentRestore(): Promise<string> {
  const queued = await queryGraphQL<{
    restoreLifecycleUnit: { jobId: string; lifecycleUnitId: string; status: string; operation: string };
  }>(
    `mutation($orgId: ID!, $unitId: ID!) {
      restoreLifecycleUnit(orgId: $orgId, unitId: $unitId) {
        jobId lifecycleUnitId status operation
      }
    }`,
    { orgId: ORG_ID, unitId: UNIT_A_ID },
  );

  expect(queued.errors).toBeUndefined();
  expect(queued.data?.restoreLifecycleUnit).toMatchObject({
    lifecycleUnitId: UNIT_A_ID,
    status: "QUEUED",
    operation: "RESTORE",
  });
  expect(JSON.stringify(queued.data)).not.toMatch(/root_resource|rootFolder|root_folder/i);
  const jobId = queued.data?.restoreLifecycleUnit.jobId;
  expect(jobId).toBeTruthy();
  return jobId!;
}

async function waitForLifecycleJob(jobId: string): Promise<{ jobId: string; lifecycleUnitId: string; status: string }> {
  const deadline = Date.now() + 15_000;
  let lastStatus = "not returned";

  while (Date.now() < deadline) {
    const result = await queryGraphQL<{ lifecycleJob: { jobId: string; lifecycleUnitId: string; status: string } }>(
      `query($orgId: ID!, $jobId: ID!) {
        lifecycleJob(orgId: $orgId, jobId: $jobId) { jobId lifecycleUnitId status }
      }`,
      { orgId: ORG_ID, jobId },
    );
    expect(result.errors).toBeUndefined();
    const job = result.data?.lifecycleJob;
    expect(job).toBeDefined();
    lastStatus = job!.status;
    if (job!.status === "SUCCEEDED") return job!;
    expect(["QUEUED", "RUNNING"]).toContain(job!.status);
    await new Promise((resolve) => setTimeout(resolve, 100));
  }

  throw new Error(`Lifecycle worker did not finish restore job ${jobId}; last GraphQL status was ${lastStatus}`);
}

async function removeFixtures(): Promise<void> {
  await assetDb.query("DELETE FROM asset_lifecycle_jobs WHERE unit_id = ANY($1::uuid[])", [[UNIT_A_ID, UNIT_B_ID]]);
  await assetDb.query("DELETE FROM asset_lifecycle_units WHERE id = ANY($1::uuid[])", [[UNIT_A_ID, UNIT_B_ID]]);
  await assetDb.query("DELETE FROM folders WHERE id = ANY($1::uuid[])", [[FOLDER_A_ID, FOLDER_B_ID]]);
}

async function seedNestedLifecycleUnits(): Promise<void> {
  await assetDb.query(
    `INSERT INTO folders (id, org_id, path, name, created_by, updated_by)
     VALUES
       ($1, $3, $4::ltree, 'KAN-83 parent A', $5, $5),
       ($2, $3, $6::ltree, 'KAN-83 independent child B', $5, $5)`,
    [FOLDER_A_ID, FOLDER_B_ID, ORG_ID, ROOT_PATH, USER_ADMIN, CHILD_PATH],
  );
  await assetDb.query(
    `INSERT INTO asset_lifecycle_units
       (id, org_id, root_resource_type, root_resource_id, root_folder_path, original_parent_path, state, requested_by, delete_completed_at, retention_until)
     VALUES
       ($1, $5, 'FOLDER', $3, $6::ltree, 'root'::ltree, 'DELETED', $7, now(), now() + interval '30 days'),
       ($2, $5, 'FOLDER', $4, $8::ltree, $6::ltree, 'DELETED', $7, now(), now() + interval '30 days')`,
    [UNIT_A_ID, UNIT_B_ID, FOLDER_A_ID, FOLDER_B_ID, ORG_ID, ROOT_PATH, USER_ADMIN, CHILD_PATH],
  );
  await assetDb.query(
    `UPDATE folders
     SET deleted_at = now(), lifecycle_unit_id = CASE id
       WHEN $1::uuid THEN $3::uuid
       WHEN $2::uuid THEN $4::uuid
     END
     WHERE id = ANY($5::uuid[])`,
    [FOLDER_A_ID, FOLDER_B_ID, UNIT_A_ID, UNIT_B_ID, [FOLDER_A_ID, FOLDER_B_ID]],
  );
}

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

beforeAll(async () => {
  assetDb = new Client({ connectionString: process.env.ASSET_DB_URL });
  await assetDb.connect();
  app = await buildServer();
  await app.ready();
});

afterEach(async () => {
  await removeFixtures();
});

afterAll(async () => {
  await removeFixtures();
  if (app) await app.close();
  await assetDb.end();
  await prisma.$disconnect();
});

describe("KAN-83 lifecycle-unit restore GraphQL to PostgreSQL E2E", () => {
  test("blocks an independently deleted child while its original parent remains hidden", async () => {
    await seedNestedLifecycleUnits();

    const result = await queryGraphQL<unknown>(
      `mutation($orgId: ID!, $unitId: ID!) {
        restoreLifecycleUnit(orgId: $orgId, unitId: $unitId) { jobId status }
      }`,
      { orgId: ORG_ID, unitId: UNIT_B_ID },
    );

    expect(result.data).toBeNull();
    expectAssetError(result, {
      code: "RESTORE_PARENT_DELETED",
      number: 3013,
      message: "Restore the parent folder before restoring this item",
    });
    const jobs = await assetDb.query<{ count: number }>(
      "SELECT COUNT(*)::int AS count FROM asset_lifecycle_jobs WHERE unit_id = $1",
      [UNIT_B_ID],
    );
    const state = await assetDb.query<{ state: string }>("SELECT state FROM asset_lifecycle_units WHERE id = $1", [
      UNIT_B_ID,
    ]);
    expect(jobs.rows[0].count).toBe(0);
    expect(state.rows[0].state).toBe("DELETED");
  });

  test.skipIf(lifecycleWorkerEnabled)(
    "queues parent restore through GraphQL and keeps internal root facts private",
    async () => {
      await seedNestedLifecycleUnits();
      const jobId = await queueParentRestore();

      const persisted = await assetDb.query<{ status: string; root_folder_id: string; count: number }>(
        `SELECT status, root_folder_id::text, COUNT(*) OVER ()::int AS count
       FROM asset_lifecycle_jobs WHERE id = $1`,
        [jobId],
      );
      expect(persisted.rows[0]).toMatchObject({ status: "QUEUED", root_folder_id: FOLDER_A_ID, count: 1 });
      const rootVisibility = await assetDb.query<{ count: number }>(
        "SELECT COUNT(*)::int AS count FROM folders WHERE id = $1 AND deleted_at IS NOT NULL",
        [FOLDER_A_ID],
      );
      expect(rootVisibility.rows[0].count).toBe(1);

      const status = await queryGraphQL<{ lifecycleJob: { jobId: string; status: string; lifecycleUnitId: string } }>(
        `query($orgId: ID!, $jobId: ID!) {
        lifecycleJob(orgId: $orgId, jobId: $jobId) { jobId lifecycleUnitId status }
      }`,
        { orgId: ORG_ID, jobId },
      );
      expect(status.errors).toBeUndefined();
      expect(status.data?.lifecycleJob).toEqual({ jobId, lifecycleUnitId: UNIT_A_ID, status: "QUEUED" });
    },
  );

  test.skipIf(!lifecycleWorkerEnabled)("completes the GraphQL restore through the real lifecycle worker", async () => {
    await seedNestedLifecycleUnits();
    const jobId = await queueParentRestore();

    const completed = await waitForLifecycleJob(jobId);
    expect(completed).toEqual({ jobId, lifecycleUnitId: UNIT_A_ID, status: "SUCCEEDED" });

    const root = await assetDb.query<{ count: number }>(
      "SELECT COUNT(*)::int AS count FROM folders WHERE id = $1 AND deleted_at IS NULL AND lifecycle_unit_id IS NULL",
      [FOLDER_A_ID],
    );
    const unit = await assetDb.query<{ state: string }>("SELECT state FROM asset_lifecycle_units WHERE id = $1", [
      UNIT_A_ID,
    ]);
    const independentChild = await assetDb.query<{ count: number }>(
      "SELECT COUNT(*)::int AS count FROM folders WHERE id = $1 AND deleted_at IS NOT NULL AND lifecycle_unit_id = $2",
      [FOLDER_B_ID, UNIT_B_ID],
    );
    expect(root.rows[0].count).toBe(1);
    expect(unit.rows[0].state).toBe("RESTORED");
    expect(independentChild.rows[0].count).toBe(1);
  });
});
