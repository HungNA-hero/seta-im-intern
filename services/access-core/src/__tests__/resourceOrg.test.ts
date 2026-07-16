import { describe, expect, test, vi, beforeEach } from "vitest";

const { mockAssetFetch, mockAssetPath } = vi.hoisted(() => ({
  mockAssetFetch: vi.fn(),
  mockAssetPath: vi.fn(),
}));

vi.mock("../clients/assetClient", () => ({
  assetFetch: mockAssetFetch,
  assetPath: mockAssetPath,
  throwGoError: vi.fn(),
  FOLDERS_PATH: "/internal/api/v1/folders",
  METADATA_PATH: "/internal/api/v1/metadata-items",
}));

import { assertResourceInOrg } from "../clients/resourceOrg";

describe("assertResourceInOrg", () => {
  beforeEach(() => {
    vi.resetAllMocks();
    mockAssetPath.mockReturnValue("/target");
  });

  test("maps a missing metadata target to METADATA_NOT_FOUND", async () => {
    mockAssetFetch.mockResolvedValue(new Response(null, { status: 404 }));

    await expect(
      assertResourceInOrg("metadata_item", "metadata-1", "org-1", "user-1"),
    ).rejects.toMatchObject({ extensions: { code: "METADATA_NOT_FOUND" } });
  });

  test("maps a missing folder target to FOLDER_NOT_FOUND", async () => {
    mockAssetFetch.mockResolvedValue(new Response(null, { status: 404 }));

    await expect(
      assertResourceInOrg("folder", "folder-1", "org-1", "user-1"),
    ).rejects.toMatchObject({ extensions: { code: "FOLDER_NOT_FOUND" } });
  });
});
