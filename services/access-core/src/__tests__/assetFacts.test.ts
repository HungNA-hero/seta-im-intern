import { describe, expect, test, vi } from "vitest";
import { createAssetFactReader, FolderMeta } from "../clients/assetFacts";

describe("createAssetFactReader", () => {
  test("loads folder facts through injected transport and cache ports", async () => {
    const transport = {
      request: vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ folder: { path: "root.child" } }), {
          status: 200,
        }),
      ),
    };
    const readFolderCache = vi.fn(async (_orgId: string, _id: string, loader: () => Promise<FolderMeta | null>) =>
      loader(),
    );
    const reader = createAssetFactReader({
      transport,
      paths: {
        folders: "/folders",
        metadata: "/metadata",
        restoreFolders: "/restore/folders",
        restoreMetadata: "/restore/metadata",
      },
      buildPath: (base, params) => `${base}?orgId=${params.orgId}&id=${params.id}`,
      throwResponseError: vi.fn(),
      invalidFact: () => {
        throw new Error("invalid fact");
      },
      decodeFolderList: vi.fn(),
      readFolderCache,
      readMetadataCache: vi.fn(),
      runSingleFlight: (_key, loader) => loader(),
    });

    await expect(reader.getFolderMeta("org-1", "user-1", "folder-1")).resolves.toEqual({ path: "root.child" });
    expect(readFolderCache).toHaveBeenCalledTimes(1);
    expect(transport.request).toHaveBeenCalledWith("/folders?orgId=org-1&id=folder-1", {
      orgId: "org-1",
      userId: "user-1",
    });
  });
});
