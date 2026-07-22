import { beforeEach, describe, expect, test, vi } from "vitest";
import { createCanDoMock } from "./helpers/canDoMock";

const { mockCanDo, mockFilterAllowedResourceIds } = vi.hoisted(() => ({
  mockCanDo: vi.fn(),
  mockFilterAllowedResourceIds: vi.fn(),
}));

vi.mock("../authz/decision", () =>
  createCanDoMock(mockCanDo, mockFilterAllowedResourceIds),
);

import { config } from "../config";
import type { GraphQLContext } from "../graphql/context";
import {
  getFolderDeletionJob,
  previewFolderDeletion,
} from "../usecase/folderDeletionUsecase";

const mockFetch = vi.fn();
vi.stubGlobal("fetch", mockFetch);

function context(overrides: Partial<GraphQLContext> = {}): GraphQLContext {
  return {
    userId: "user-1",
    currentOrgId: "org-1",
    isMember: true,
    roles: ["org_admin"],
    olpEnabled: false,
    ...overrides,
  };
}

function job() {
  return {
    id: "job-1",
    org_id: "org-1",
    root_folder_id: "folder-1",
    requested_by: "user-1",
    status: "queued",
    active_folder_count: 2,
    active_metadata_count: 3,
    tombstone_folder_count: 0,
    tombstone_metadata_count: 1,
    deleted_folder_count: 0,
    deleted_metadata_count: 0,
    attempts: 0,
    manual_retries: 0,
  };
}

describe("folder deletion usecase", () => {
  beforeEach(() => {
    mockFetch.mockReset();
    mockCanDo.mockResolvedValue({ allowed: true, reason: null });
  });

  test("requires delete authorization before returning a confirmation preview", async () => {
    mockFetch.mockResolvedValue(
      new Response(
        JSON.stringify({
          preview: {
            id: "preview-1",
            root_folder_id: "folder-1",
            active_folder_count: 2,
            active_metadata_count: 3,
            tombstone_folder_count: 0,
            tombstone_metadata_count: 1,
            total_rows: 6,
            confirmation_token: "token",
            expires_at: "2026-07-22T03:00:00Z",
          },
        }),
        { status: 200 },
      ),
    );

    const preview = await previewFolderDeletion(context(), "org-1", "folder-1");

    expect(mockCanDo).toHaveBeenCalledWith(
      "user-1",
      "delete",
      "folder",
      "folder-1",
      "org-1",
    );
    expect(preview.totalRows).toBe(6);
    expect(mockFetch).toHaveBeenCalledWith(
      `${config.goAssetUrl}/internal/api/v1/folder-deletions/preview?orgId=org-1&folderId=folder-1`,
      {
        method: "POST",
        headers: {
          "X-User-Id": "user-1",
          "X-Org-Id": "org-1",
          Authorization: `Bearer ${config.assetInternalApiToken}`,
        },
        signal: expect.any(AbortSignal),
      },
    );
  });

  test("forwards the trusted org-admin signal only for job administration", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ job: job() }), { status: 200 }),
    );

    const result = await getFolderDeletionJob(context(), "org-1", "job-1");

    expect(result.status).toBe("queued");
    expect(mockFetch).toHaveBeenCalledWith(
      `${config.goAssetUrl}/internal/api/v1/folder-deletions/jobs?orgId=org-1&id=job-1`,
      {
        method: "GET",
        headers: {
          "X-User-Id": "user-1",
          "X-Org-Id": "org-1",
          "X-Org-Admin": "true",
          Authorization: `Bearer ${config.assetInternalApiToken}`,
        },
        signal: expect.any(AbortSignal),
      },
    );
  });
});
