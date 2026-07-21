import {
  assetPath,
  FOLDERS_PATH,
  throwGoError,
  unwrap204,
  unwrapEnvelope,
  unwrapListEnvelope,
} from "../clients/assetClient";
import { filterVisible } from "../authz/decision";
import {
  FolderNode,
  folderHierarchy,
  GoFolder,
  toFolder,
} from "../domain/folder";
import { badUserInput, forbidden } from "../errors/factories";
import {
  assertAuthenticated,
  assertOrgContext,
  assertOrgMember,
  GraphQLContext,
} from "../graphql/context";
import { authorizedFetch, Precondition } from "./assetProxy";

type FolderWithSubtree = FolderNode & { subtreeNodes?: FolderNode[] };

function assertNotRootFolder(
  id: string,
  orgId: string,
  action: string,
): void {
  if (id === orgId) {
    throw forbidden(`Cannot ${action} root folder`);
  }
}

async function fetchFolderList(
  ctx: GraphQLContext,
  orgId: string,
  path: string,
): Promise<FolderNode[]> {
  const response = await authorizedFetch(ctx, orgId, [], path);
  return unwrapListEnvelope(
    response,
    "folders",
    toFolder,
    "Failed to fetch folders",
  );
}

function attachSubtreeCache(visible: FolderNode[]): FolderWithSubtree[] {
  const cached = visible as FolderWithSubtree[];
  cached.forEach((folder) => {
    folder.subtreeNodes = cached;
  });
  return cached;
}

export async function getFolder(
  ctx: GraphQLContext,
  orgId: string,
  id: string,
) {
  const response = await authorizedFetch(
    ctx,
    orgId,
    [{ action: "read", resourceType: "folder", resourceId: id }],
    assetPath(FOLDERS_PATH, { orgId, id }),
  );

  if (response.status === 404) return null;
  if (!response.ok) await throwGoError(response);
  const data = await response.json();
  return data.folder ? toFolder(data.folder as GoFolder) : null;
}

export async function listFolderTree(
  ctx: GraphQLContext,
  orgId: string,
  rootPath: string | undefined,
  withSubtree: boolean,
) {
  assertOrgMember(ctx);
  const path = assetPath(
    FOLDERS_PATH,
    rootPath ? { orgId, rootPath } : { orgId, tree: true },
  );
  const folders = await fetchFolderList(ctx, orgId, path);
  const visible = await filterVisible(
    ctx.userId,
    orgId,
    "read",
    "folder",
    folders,
    folderHierarchy,
  );
  return withSubtree ? attachSubtreeCache(visible) : visible;
}

export async function listFolderChildren(
  ctx: GraphQLContext,
  orgId: string,
  parentPath: string,
) {
  assertOrgMember(ctx);
  const path = assetPath(FOLDERS_PATH, {
    orgId,
    rootPath: parentPath,
    children: true,
  });
  const folders = await fetchFolderList(ctx, orgId, path);
  return filterVisible(
    ctx.userId,
    orgId,
    "read",
    "folder",
    folders,
    folderHierarchy,
  );
}

export async function childrenOf(
  ctx: GraphQLContext,
  parent: FolderWithSubtree,
) {
  assertAuthenticated(ctx);
  assertOrgContext(ctx, parent.orgId);

  if (parent.subtreeNodes) {
    const cache = parent.subtreeNodes;
    const prefix = `${parent.path}.`;
    return cache
      .filter(
        (folder) =>
          folder.path.startsWith(prefix) &&
          !folder.path.slice(prefix.length).includes("."),
      )
      .map((folder) => ({ ...folder, subtreeNodes: cache }));
  }

  const path = assetPath(FOLDERS_PATH, {
    orgId: parent.orgId,
    rootPath: parent.path,
    children: true,
  });
  const folders = await fetchFolderList(ctx, parent.orgId, path);
  return filterVisible(
    ctx.userId,
    parent.orgId,
    "read",
    "folder",
    folders,
    folderHierarchy,
  );
}

export async function createFolder(
  ctx: GraphQLContext,
  orgId: string,
  name: string,
  parentPath?: string,
  description?: string,
) {
  const body = {
    name,
    ...(parentPath !== undefined && { parent_path: parentPath }),
    ...(description !== undefined && { description }),
  };
  const response = await authorizedFetch(
    ctx,
    orgId,
    [{ action: "write", resourceType: "folder", resourceId: orgId }],
    assetPath(FOLDERS_PATH, { orgId }),
    { method: "POST", body },
  );
  return unwrapEnvelope(
    response,
    "folder",
    toFolder,
    "Failed to create folder",
  );
}

export async function updateFolder(
  ctx: GraphQLContext,
  orgId: string,
  id: string,
  name?: string | null,
  description?: string | null,
) {
  assertAuthenticated(ctx);
  if (name === undefined && description === undefined) {
    throw badUserInput("At least one field must be provided");
  }
  if (name === null) {
    throw badUserInput("Folder name cannot be null");
  }

  const body = {
    ...(name !== undefined && { name }),
    ...(description !== undefined && { description }),
  };
  const response = await authorizedFetch(
    ctx,
    orgId,
    [{ action: "write", resourceType: "folder", resourceId: id }],
    assetPath(FOLDERS_PATH, { orgId, id }),
    { method: "PATCH", body },
  );
  return unwrapEnvelope(
    response,
    "folder",
    toFolder,
    "Failed to update folder",
  );
}

export async function moveFolder(
  ctx: GraphQLContext,
  orgId: string,
  id: string,
  destinationParentId?: string | null,
) {
  assertAuthenticated(ctx);
  assertNotRootFolder(id, orgId, "move");
  const destinationId = destinationParentId ?? orgId;
  const require: Precondition[] = [
    { action: "write", resourceType: "folder", resourceId: id },
    {
      action: "write",
      resourceType: "folder",
      resourceId: destinationId,
    },
  ];
  const response = await authorizedFetch(
    ctx,
    orgId,
    require,
    assetPath(`${FOLDERS_PATH}/move`, { orgId, id }),
    {
      method: "PATCH",
      body: { destination_parent_id: destinationParentId ?? null },
    },
  );
  return unwrapEnvelope(
    response,
    "folder",
    toFolder,
    "Failed to move folder",
  );
}

export async function deleteFolder(
  ctx: GraphQLContext,
  orgId: string,
  id: string,
) {
  assertAuthenticated(ctx);
  assertNotRootFolder(id, orgId, "delete");
  const response = await authorizedFetch(
    ctx,
    orgId,
    [{ action: "delete", resourceType: "folder", resourceId: id }],
    assetPath(FOLDERS_PATH, { orgId, id }),
    { method: "DELETE" },
  );
  return unwrap204(response, "Failed to delete folder");
}
