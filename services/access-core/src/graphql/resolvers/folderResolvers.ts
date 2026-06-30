import { GraphQLError } from "graphql";
import { canDo } from "../../db/queries/canDo";
import { assertAuthenticated, GraphQLContext } from "../context";
import { config } from "../../config";

interface GoFolder {
  id: string;
  org_id: string;
  path: string;
  name: string;
  description: string | null;
  created_by: string;
  updated_by: string | null;
  created_at: string;
  updated_at: string;
}

function toFolder(f: GoFolder) {
  return {
    id: f.id,
    orgId: f.org_id,
    path: f.path,
    name: f.name,
    description: f.description,
    createdBy: f.created_by,
    updatedBy: f.updated_by,
    createdAt: f.created_at,
    updatedAt: f.updated_at,
  };
}

type FolderNode = ReturnType<typeof toFolder>;

const GO_ERROR_CODES: Record<number, string> = {
  400: "BAD_USER_INPUT",
  401: "UNAUTHENTICATED",
  403: "FORBIDDEN",
  404: "NOT_FOUND",
  409: "CONFLICT",
};

function goHeaders(
  userId: string,
  orgId: string,
): Record<string, string> {
  return { "X-User-Id": userId, "X-Org-Id": orgId };
}

async function assertFolderPermission(
  userId: string,
  orgId: string,
  resourceId: string,
  action: "read" | "write" | "delete" | "manage_permissions",
) {
  const { allowed, reason } = await canDo(
    userId,
    action,
    "folder",
    resourceId,
    orgId,
  );
  if (!allowed) {
    throw new GraphQLError(reason ?? "Forbidden", {
      extensions: { code: "FORBIDDEN" },
    });
  }
}

async function fetchFolderList(
  url: string,
  userId: string,
  orgId: string,
): Promise<GoFolder[]> {
  const resp = await fetch(url, { headers: goHeaders(userId, orgId) });
  if (!resp.ok) {
    const code = GO_ERROR_CODES[resp.status] ?? "INTERNAL_SERVER_ERROR";
    throw new GraphQLError(`Failed to fetch folders: ${resp.statusText}`, {
      extensions: { code },
    });
  }
  const data = await resp.json();
  return (data.folders ?? []) as GoFolder[];
}

async function handleGoResponse(
  res: Response,
  defaultMessage: string,
): Promise<FolderNode> {
  if (res.ok) {
    const data = await res.json();
    if (!data.folder) {
      throw new GraphQLError(`${defaultMessage}: unexpected response format`, {
        extensions: { code: "INTERNAL_SERVER_ERROR" },
      });
    }
    return toFolder(data.folder as GoFolder);
  }
  const code = GO_ERROR_CODES[res.status] ?? "INTERNAL_SERVER_ERROR";
  throw new GraphQLError(`${defaultMessage}: ${res.statusText}`, {
    extensions: { code },
  });
}

export const folderResolvers = {
  Query: {
    folder: async (
      _: unknown,
      { orgId, id }: { orgId: string; id: string },
      ctx: GraphQLContext,
    ) => {
      assertAuthenticated(ctx);
      await assertFolderPermission(ctx.userId, orgId, id, "read");

      const resp = await fetch(
        `${config.goAssetUrl}/internal/api/v1/folders?orgId=${encodeURIComponent(orgId)}&id=${encodeURIComponent(id)}`,
        { headers: goHeaders(ctx.userId, orgId) },
      );

      if (resp.status === 404) return null;
      if (!resp.ok) {
        const code = GO_ERROR_CODES[resp.status] ?? "INTERNAL_SERVER_ERROR";
        throw new GraphQLError(`Failed to fetch folder: ${resp.statusText}`, {
          extensions: { code },
        });
      }

      const data = await resp.json();
      return data.folder ? toFolder(data.folder as GoFolder) : null;
    },

    folderTree: async (
      _: unknown,
      { orgId, rootPath }: { orgId: string; rootPath?: string },
      ctx: GraphQLContext,
    ) => {
      assertAuthenticated(ctx);
      await assertFolderPermission(ctx.userId, orgId, orgId, "read");

      let url = `${config.goAssetUrl}/internal/api/v1/folders?orgId=${orgId}`;
      if (rootPath) url += `&rootPath=${encodeURIComponent(rootPath)}`;

      const folders = (await fetchFolderList(url, ctx.userId, orgId)).map(toFolder) as (FolderNode & { _subtreeNodes: FolderNode[] })[];
      folders.forEach(f => { f._subtreeNodes = folders; });
      return folders;
    },

    folderChildren: async (
      _: unknown,
      { orgId, parentPath }: { orgId: string; parentPath: string },
      ctx: GraphQLContext,
    ) => {
      assertAuthenticated(ctx);
      await assertFolderPermission(ctx.userId, orgId, orgId, "read");

      const url = `${config.goAssetUrl}/internal/api/v1/folders?orgId=${orgId}&rootPath=${encodeURIComponent(parentPath)}&children=true`;
      return (await fetchFolderList(url, ctx.userId, orgId)).map(toFolder);
    },
  },

  Folder: {
    children: async (
      parent: FolderNode & { _subtreeNodes?: FolderNode[] },
      _: unknown,
      ctx: GraphQLContext,
    ) => {
      assertAuthenticated(ctx);

      if (parent._subtreeNodes) {
        const cache = parent._subtreeNodes;
        const prefix = parent.path + ".";
        const kids = cache.filter(
          f => f.path.startsWith(prefix) && !f.path.slice(prefix.length).includes("."),
        );
        return kids.map(f => ({ ...f, _subtreeNodes: cache }));
      }

      console.warn(`[folderResolvers] Folder.children: no subtree cache on parent "${parent.path}", falling back to HTTP`);
      const url = `${config.goAssetUrl}/internal/api/v1/folders?orgId=${parent.orgId}&rootPath=${encodeURIComponent(parent.path)}&children=true`;
      return (await fetchFolderList(url, ctx.userId, parent.orgId)).map(toFolder);
    },
  },

  Mutation: {
    createFolder: async (
      _: unknown,
      {
        orgId,
        parentPath,
        name,
        description,
      }: {
        orgId: string;
        parentPath?: string;
        name: string;
        description?: string;
      },
      ctx: GraphQLContext,
    ) => {
      assertAuthenticated(ctx);
      await assertFolderPermission(ctx.userId, orgId, orgId, "write");

      const body = {
        name,
        ...(parentPath !== undefined && { parent_path: parentPath }),
        ...(description !== undefined && { description }),
      };

      const res = await fetch(
        `${config.goAssetUrl}/internal/api/v1/folders?orgId=${encodeURIComponent(orgId)}`,
        {
          method: "POST",
          headers: { ...goHeaders(ctx.userId, orgId), "Content-Type": "application/json" },
          body: JSON.stringify(body),
        },
      );
      return handleGoResponse(res, "Failed to create folder");
    },

    updateFolder: async (
      _: unknown,
      {
        orgId,
        id,
        name,
        description,
      }: {
        orgId: string;
        id: string;
        name?: string | null;
        description?: string | null;
      },
      ctx: GraphQLContext,
    ) => {
      assertAuthenticated(ctx);

      if (name === undefined && description === undefined) {
        throw new GraphQLError("At least one field must be provided", {
          extensions: { code: "BAD_USER_INPUT" },
        });
      }
      if (name === null) {
        throw new GraphQLError("Folder name cannot be null", {
          extensions: { code: "BAD_USER_INPUT" },
        });
      }

      await assertFolderPermission(ctx.userId, orgId, id, "write");

      const body = {
        ...(name !== undefined && { name }),
        ...(description !== undefined && { description }),
      };

      const res = await fetch(
        `${config.goAssetUrl}/internal/api/v1/folders?orgId=${encodeURIComponent(orgId)}&id=${encodeURIComponent(id)}`,
        {
          method: "PATCH",
          headers: { ...goHeaders(ctx.userId, orgId), "Content-Type": "application/json" },
          body: JSON.stringify(body),
        },
      );
      return handleGoResponse(res, "Failed to update folder");
    },
  },
};
