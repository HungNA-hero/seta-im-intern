import { describe, expect, test, vi } from "vitest";
import { createAssetFactReader, FolderMeta, MetadataItemMeta } from "../clients/assetFacts";
import { FOLDERS_PATH } from "../clients/assetPaths";

function passThroughCache<T>() {
  return vi.fn(async (_orgId: string, _id: string, loader: () => Promise<T | null>) => loader());
}

function createReader(response: Response) {
  const transport = { request: vi.fn().mockResolvedValue(response) };
  const readFolderCache = passThroughCache<FolderMeta>();
  const readMetadataCache = passThroughCache<MetadataItemMeta>();
  const reader = createAssetFactReader({
    transport,
    readFolderCache,
    readMetadataCache,
    runSingleFlight: (_key, loader) => loader(),
  });
  return { reader, transport, readFolderCache, readMetadataCache };
}

describe("createAssetFactReader", () => {
  test("loads folder facts through its transport and cache ports", async () => {
    const { reader, transport, readFolderCache } = createReader(
      new Response(JSON.stringify({ folder: { path: "root.child" } }), { status: 200 }),
    );

    await expect(reader.getFolderMeta("org-1", "user-1", "folder-1")).resolves.toEqual({ path: "root.child" });

    expect(readFolderCache).toHaveBeenCalledTimes(1);
    expect(transport.request).toHaveBeenCalledWith(`${FOLDERS_PATH}?orgId=org-1&id=folder-1`, {
      orgId: "org-1",
      userId: "user-1",
    });
  });

  test("treats a missing folder as an absent fact rather than an error", async () => {
    const { reader } = createReader(new Response(null, { status: 404 }));

    await expect(reader.getFolderMeta("org-1", "user-1", "folder-1")).resolves.toBeNull();
  });

  test("reads metadata facts through the metadata cache port", async () => {
    const { reader, readMetadataCache } = createReader(
      new Response(JSON.stringify({ item: { folder_id: "folder-1" } }), { status: 200 }),
    );

    await expect(reader.getMetadataMeta("org-1", "user-1", "item-1")).resolves.toEqual({ folderId: "folder-1" });
    expect(readMetadataCache).toHaveBeenCalledTimes(1);
  });

  test("takes the cached single-folder route rather than the batch route for one id", async () => {
    const { reader, transport, readFolderCache } = createReader(
      new Response(JSON.stringify({ folder: { path: "root.child" } }), { status: 200 }),
    );

    const batch = await reader.getFolderMetaBatch("org-1", "user-1", ["folder-1", "folder-1"]);

    expect(batch).toEqual(new Map([["folder-1", { path: "root.child" }]]));
    expect(readFolderCache).toHaveBeenCalledTimes(1);
    expect(transport.request).toHaveBeenCalledTimes(1);
  });

  test("returns an empty map without calling asset-core for no ids", async () => {
    const { reader, transport } = createReader(new Response(null, { status: 200 }));

    await expect(reader.getFolderMetaBatch("org-1", "user-1", [])).resolves.toEqual(new Map());
    expect(transport.request).not.toHaveBeenCalled();
  });

  test("decodes a multi-folder batch into a map keyed by folder id", async () => {
    const { reader, readFolderCache } = createReader(
      new Response(
        JSON.stringify({
          folders: [
            { id: "folder-1", path: "root.one" },
            { id: "folder-2", path: "root.two" },
          ],
        }),
        { status: 200 },
      ),
    );

    const batch = await reader.getFolderMetaBatch("org-1", "user-1", ["folder-1", "folder-2"]);

    expect(batch).toEqual(
      new Map([
        ["folder-1", { path: "root.one" }],
        ["folder-2", { path: "root.two" }],
      ]),
    );
    expect(readFolderCache).not.toHaveBeenCalled();
  });
});
