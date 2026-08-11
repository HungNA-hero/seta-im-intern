import { getFolderMetaBatch } from "../clients/assetClient";
import { ancestorIdsFromPath } from "../domain/ltreePath";

export async function loadFolderAncestorMap(
  orgId: string,
  userId: string,
  folderIds: string[],
): Promise<Map<string, string[]>> {
  const uniqueIds = [...new Set(folderIds)];
  const metaById = await getFolderMetaBatch(orgId, userId, uniqueIds);
  return new Map(
    uniqueIds.map((folderId) => {
      const folderMeta = metaById.get(folderId);
      return [folderId, folderMeta ? ancestorIdsFromPath(folderMeta.path) : []];
    }),
  );
}

export async function loadFolderAncestors(orgId: string, userId: string, folderId: string): Promise<string[]> {
  const ancestorsByFolderId = await loadFolderAncestorMap(orgId, userId, [folderId]);
  return ancestorsByFolderId.get(folderId) ?? [];
}
