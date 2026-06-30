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
  const resp = await fetch(url, {
    headers: { "X-User-Id": userId, "X-Org-Id": orgId },
  });
  if (!resp.ok) {
    throw new GraphQLError(`Failed to fetch folders: ${resp.statusText}`, {
      extensions: { code: "INTERNAL_SERVER_ERROR" },
    });
  }
  const data = await resp.json();
  return (data.folders ?? []) as GoFolder[];
}

export const folderResolvers = {
  Query: {
    folder: async (_: unknown, { id }: { id: string }, ctx: GraphQLContext) => {
      assertAuthenticated(ctx);
      await assertFolderPermission(
        ctx.userId,
        ctx.currentOrgId ?? "",
        id,
        "read",
      );

      const resp = await fetch(
        `${config.goAssetUrl}/internal/api/v1/folders?id=${id}`,
        {
          headers: {
            "X-User-Id": ctx.userId,
            "X-Org-Id": ctx.currentOrgId ?? "",
          },
        },
      );

      if (resp.status === 404) return null;
      if (!resp.ok) {
        throw new GraphQLError(`Failed to fetch folder: ${resp.statusText}`, {
          extensions: { code: "INTERNAL_SERVER_ERROR" },
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

      return (await fetchFolderList(url, ctx.userId, orgId)).map(toFolder);
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
      parent: { orgId: string; path: string },
      _: unknown,
      ctx: GraphQLContext,
    ) => {
      assertAuthenticated(ctx);
      const url = `${config.goAssetUrl}/internal/api/v1/folders?orgId=${parent.orgId}&rootPath=${encodeURIComponent(parent.path)}&children=true`;
      return (await fetchFolderList(url, ctx.userId, parent.orgId)).map(
        toFolder,
      );
    },
  },
};
