import { AssetTransport } from "./assetTransport";

export interface FolderMeta {
  path: string;
}

export interface MetadataItemMeta {
  folderId: string;
}

export interface FolderRestoreAuthorizationFact {
  id: string;
  path: string;
}

export interface MetadataRestoreAuthorizationFact {
  id: string;
  folderId: string;
  folderPath: string;
}

export interface AssetFactReader {
  getFolderRestoreAuthorizationFact(
    orgId: string,
    userId: string,
    id: string,
  ): Promise<FolderRestoreAuthorizationFact | null>;
  getMetadataRestoreAuthorizationFact(
    orgId: string,
    userId: string,
    id: string,
  ): Promise<MetadataRestoreAuthorizationFact | null>;
  getFolderMeta(orgId: string, userId: string, id: string): Promise<FolderMeta | null>;
  getFolderMetaBatch(orgId: string, userId: string, ids: string[]): Promise<Map<string, FolderMeta>>;
  getMetadataMeta(orgId: string, userId: string, id: string): Promise<MetadataItemMeta | null>;
}

export interface AssetFactReaderDependencies {
  transport: AssetTransport;
  paths: {
    folders: string;
    metadata: string;
    restoreFolders: string;
    restoreMetadata: string;
  };
  buildPath(base: string, params: Record<string, string | string[] | undefined>): string;
  throwResponseError(response: Response): Promise<never>;
  invalidFact(): never;
  decodeFolderList(response: Response): Promise<Array<{ id: string; path: string }>>;
  readFolderCache(orgId: string, id: string, loader: () => Promise<FolderMeta | null>): Promise<FolderMeta | null>;
  readMetadataCache(
    orgId: string,
    id: string,
    loader: () => Promise<MetadataItemMeta | null>,
  ): Promise<MetadataItemMeta | null>;
  runSingleFlight<T>(key: string, loader: () => Promise<T>): Promise<T>;
}

export function createAssetFactReader(dependencies: AssetFactReaderDependencies): AssetFactReader {
  async function request(path: string, orgId: string, userId: string): Promise<Response> {
    return dependencies.transport.request(path, { orgId, userId });
  }

  async function fetchFolderMeta(orgId: string, userId: string, id: string): Promise<FolderMeta | null> {
    const response = await request(dependencies.buildPath(dependencies.paths.folders, { orgId, id }), orgId, userId);
    if (response.status === 404) return null;
    if (!response.ok) await dependencies.throwResponseError(response);
    const data = (await response.json()) as { folder?: { path: string } };
    return data.folder ? { path: data.folder.path } : null;
  }

  async function fetchMetadataMeta(orgId: string, userId: string, id: string): Promise<MetadataItemMeta | null> {
    const response = await request(dependencies.buildPath(dependencies.paths.metadata, { orgId, id }), orgId, userId);
    if (response.status === 404) return null;
    if (!response.ok) await dependencies.throwResponseError(response);
    const data = (await response.json()) as {
      item?: { folder_id: string };
    };
    return data.item ? { folderId: data.item.folder_id } : null;
  }

  const reader: AssetFactReader = {
    async getFolderRestoreAuthorizationFact(orgId, userId, id) {
      const response = await request(
        dependencies.buildPath(dependencies.paths.restoreFolders, {
          orgId,
          id,
        }),
        orgId,
        userId,
      );
      if (response.status === 404) return null;
      if (!response.ok) await dependencies.throwResponseError(response);
      const data = (await response.json()) as {
        fact?: { id?: unknown; path?: unknown };
      };
      if (typeof data.fact?.id !== "string" || typeof data.fact.path !== "string") {
        dependencies.invalidFact();
      }
      return { id: data.fact.id, path: data.fact.path };
    },

    async getMetadataRestoreAuthorizationFact(orgId, userId, id) {
      const response = await request(
        dependencies.buildPath(dependencies.paths.restoreMetadata, {
          orgId,
          id,
        }),
        orgId,
        userId,
      );
      if (response.status === 404) return null;
      if (!response.ok) await dependencies.throwResponseError(response);
      const data = (await response.json()) as {
        fact?: {
          id?: unknown;
          folder_id?: unknown;
          folder_path?: unknown;
        };
      };
      if (
        typeof data.fact?.id !== "string" ||
        typeof data.fact.folder_id !== "string" ||
        typeof data.fact.folder_path !== "string"
      ) {
        dependencies.invalidFact();
      }
      return {
        id: data.fact.id,
        folderId: data.fact.folder_id,
        folderPath: data.fact.folder_path,
      };
    },

    getFolderMeta(orgId, userId, id) {
      return dependencies.readFolderCache(orgId, id, () =>
        dependencies.runSingleFlight(`folder-meta:${orgId}:${id}`, () => fetchFolderMeta(orgId, userId, id)),
      );
    },

    async getFolderMetaBatch(orgId, userId, ids) {
      const uniqueIds = [...new Set(ids)];
      if (uniqueIds.length === 0) return new Map();
      if (uniqueIds.length === 1) {
        const meta = await reader.getFolderMeta(orgId, userId, uniqueIds[0]);
        return meta ? new Map([[uniqueIds[0], meta]]) : new Map();
      }

      const response = await request(
        dependencies.buildPath(dependencies.paths.folders, {
          orgId,
          id: uniqueIds,
        }),
        orgId,
        userId,
      );
      const folders = await dependencies.decodeFolderList(response);
      return new Map(folders.map((folder) => [folder.id, { path: folder.path }]));
    },

    getMetadataMeta(orgId, userId, id) {
      return dependencies.readMetadataCache(orgId, id, () =>
        dependencies.runSingleFlight(`item-meta:${orgId}:${id}`, () => fetchMetadataMeta(orgId, userId, id)),
      );
    },
  };

  return reader;
}
