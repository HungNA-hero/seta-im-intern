import { beforeEach, describe, expect, test, vi } from "vitest";
import { createCanDoMock } from "./helpers/canDoMock";

const { mockCanDo } = vi.hoisted(() => ({ mockCanDo: vi.fn() }));
const { mockFilterAllowedResourceIds } = vi.hoisted(() => ({ mockFilterAllowedResourceIds: vi.fn() }));

vi.mock("../authz/decision", () => createCanDoMock(mockCanDo, mockFilterAllowedResourceIds));
vi.mock("../config", () => ({
  config: { goAssetUrl: "http://go-mock", assetInternalApiToken: "test-token" },
  ASSET_FETCH_TIMEOUT_MS: 3000,
}));
vi.mock("../db/prisma", () => ({
  prisma: {
    user: { findUnique: vi.fn() },
    objectPermission: { deleteMany: vi.fn() },
  },
}));

const mockFetch = vi.fn();
vi.stubGlobal("fetch", mockFetch);

import { lifecycleResolvers } from "../graphql/resolvers/lifecycleResolvers";
import type { GraphQLContext } from "../graphql/context";

const orgId = "00000000-0000-0000-0000-000000000001";
const unitId = "10000000-0000-0000-0000-000000000001";
const rootFolderId = "20000000-0000-0000-0000-000000000001";
const jobId = "30000000-0000-0000-0000-000000000001";

function ctx(overrides: Partial<GraphQLContext> = {}): GraphQLContext {
  return {
    userId: "40000000-0000-0000-0000-000000000001",
    currentOrgId: orgId,
    isMember: true,
    roles: [],
    olpEnabled: false,
    ...overrides,
  };
}

function ok(payload: unknown, status = 200) {
  return { ok: true, status, json: async () => payload };
}

function restoreFact() {
  return {
    fact: {
      unit_id: unitId,
      root_resource_type: "FOLDER",
      root_resource_id: rootFolderId,
      root_folder_id: rootFolderId,
      root_folder_path: "root.deleted",
    },
  };
}

function lifecycleJob() {
  return {
    job: {
      id: jobId,
      org_id: orgId,
      unit_id: unitId,
      root_resource_type: "FOLDER",
      root_resource_id: rootFolderId,
      root_folder_id: rootFolderId,
      root_folder_path: "root.deleted",
      requested_by: "40000000-0000-0000-0000-000000000001",
      operation: "RESTORE",
      status: "QUEUED",
      attempts: 0,
      failure_code: null,
      queued_at: "2026-08-14T07:00:00Z",
      started_at: null,
      completed_at: null,
    },
  };
}

beforeEach(() => {
  vi.resetAllMocks();
  mockCanDo.mockResolvedValue({ allowed: true, reason: null });
  mockFilterAllowedResourceIds.mockResolvedValue(new Set());
});

describe("Lifecycle restore GraphQL boundary", () => {
  test("authorizes current write on the private root fact before queueing a restore", async () => {
    mockFetch.mockResolvedValueOnce(ok(restoreFact()));
    mockFetch.mockResolvedValueOnce(ok(lifecycleJob(), 202));

    const result = await lifecycleResolvers.Mutation.restoreLifecycleUnit(undefined, { orgId, unitId }, ctx());

    expect(result).toMatchObject({ jobId, lifecycleUnitId: unitId, operation: "RESTORE", status: "QUEUED" });
    expect(mockCanDo).toHaveBeenCalledWith({
      userId: "40000000-0000-0000-0000-000000000001",
      action: "write",
      resourceType: "folder",
      resourceId: rootFolderId,
      orgId,
      ancestorIds: [],
    });
    expect(mockFetch).toHaveBeenNthCalledWith(
      2,
      `http://go-mock/internal/api/v1/lifecycle-units/restore?orgId=${orgId}&unitId=${unitId}`,
      expect.objectContaining({ method: "POST" }),
    );
  });

  test("does not queue when current write is denied", async () => {
    mockFetch.mockResolvedValueOnce(ok(restoreFact()));
    mockCanDo.mockResolvedValueOnce({ allowed: false, reason: "no write" });

    await expect(
      lifecycleResolvers.Mutation.restoreLifecycleUnit(undefined, { orgId, unitId }, ctx()),
    ).rejects.toMatchObject({
      extensions: { code: "FORBIDDEN" },
    });
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  test("authorizes a job status against its trusted root before exposing it", async () => {
    mockFetch.mockResolvedValueOnce(ok(lifecycleJob()));

    const result = await lifecycleResolvers.Query.lifecycleJob(undefined, { orgId, jobId }, ctx());

    expect(result).toMatchObject({ jobId, status: "QUEUED" });
    expect(result).not.toHaveProperty("rootResourceId");
    expect(mockCanDo).toHaveBeenCalledWith(
      expect.objectContaining({ action: "write", resourceType: "folder", resourceId: rootFolderId, orgId }),
    );
  });
});
