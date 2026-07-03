import { GraphQLError } from "graphql";
import {
  assertAuthenticated,
  assertCan,
  assertOrgContext,
  assertOrgMember,
  GraphQLContext,
} from "../context";
import { filterVisible } from "../../db/queries/canDo";
import {
  assetFetch,
  assetPath,
  throwGoError,
  unwrapEnvelope,
  unwrapListEnvelope,
  unwrap204,
  FOLDERS_PATH,
} from "../../clients/assetClient";

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

function assertNotRootFolder(id: string, orgId: string, action: string): void {
  if (id === orgId) {
    throw new GraphQLError(`Cannot ${action} root folder`, {
      extensions: { code: "FORBIDDEN" },
    });
  }
}

async function fetchFolderList(
  path: string,
  userId: string,
  orgId: string,
): Promise<FolderNode[]> {
  const resp = await assetFetch(path, { userId, orgId });
  return unwrapListEnvelope(
    resp,
    "folders",
    toFolder,
    "Failed to fetch folders",
  );
}

export const folderResolvers = {
  Query: {
    folder: async (
      _: unknown,
      { orgId, id }: { orgId: string; id: string },
      ctx: GraphQLContext,
    ) => {
      assertAuthenticated(ctx);
      await assertCan(ctx.userId, "read", "folder", id, orgId);

      const resp = await assetFetch(assetPath(FOLDERS_PATH, { orgId, id }), {
        userId: ctx.userId,
        orgId,
      });

      if (resp.status === 404) return null;
      if (!resp.ok) throwGoError(resp, "Failed to fetch folder");

      const data = await resp.json();
      return data.folder ? toFolder(data.folder as GoFolder) : null;
    },

    folderTree: async (
      _: unknown,
      { orgId, rootPath }: { orgId: string; rootPath?: string },
      ctx: GraphQLContext,
    ) => {
      assertOrgMember(ctx);

      const path = assetPath(
        FOLDERS_PATH,
        rootPath ? { orgId, rootPath } : { orgId, tree: true },
      );

      const folders = await fetchFolderList(path, ctx.userId, orgId);
      const visible = await filterVisible(ctx.userId, orgId, "read", "folder", folders);

      const cached = visible as (FolderNode & {
        subtreeNodes: FolderNode[];
      })[];
      cached.forEach((folder) => {
        folder.subtreeNodes = cached;
      });
      return visible;
    },

    folderChildren: async (
      _: unknown,
      { orgId, parentPath }: { orgId: string; parentPath: string },
      ctx: GraphQLContext,
    ) => {
      assertOrgMember(ctx);

      const path = assetPath(FOLDERS_PATH, {
        orgId,
        rootPath: parentPath,
        children: true,
      });
      const folders = await fetchFolderList(path, ctx.userId, orgId);
      return filterVisible(ctx.userId, orgId, "read", "folder", folders);
    },
  },

  Folder: {
    children: async (
      parent: FolderNode & { subtreeNodes?: FolderNode[] },
      _: unknown,
      ctx: GraphQLContext,
    ) => {
      assertAuthenticated(ctx);
      assertOrgContext(ctx, parent.orgId);

      if (parent.subtreeNodes) {
        const cache = parent.subtreeNodes;
        const prefix = parent.path + ".";
        const kids = cache.filter(
          (f) =>
            f.path.startsWith(prefix) &&
            !f.path.slice(prefix.length).includes("."),
        );
        return kids.map((f) => ({ ...f, subtreeNodes: cache }));
      }

      const path = assetPath(FOLDERS_PATH, {
        orgId: parent.orgId,
        rootPath: parent.path,
        children: true,
      });
      const folders = await fetchFolderList(path, ctx.userId, parent.orgId);
      return filterVisible(ctx.userId, parent.orgId, "read", "folder", folders);
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
      await assertCan(ctx.userId, "write", "folder", orgId, orgId);

      const body = {
        name,
        ...(parentPath !== undefined && { parent_path: parentPath }),
        ...(description !== undefined && { description }),
      };

      const res = await assetFetch(assetPath(FOLDERS_PATH, { orgId }), {
        userId: ctx.userId,
        orgId,
        method: "POST",
        body,
      });
      return unwrapEnvelope(res, "folder", toFolder, "Failed to create folder");
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

      await assertCan(ctx.userId, "write", "folder", id, orgId);

      const body = {
        ...(name !== undefined && { name }),
        ...(description !== undefined && { description }),
      };

      const res = await assetFetch(assetPath(FOLDERS_PATH, { orgId, id }), {
        userId: ctx.userId,
        orgId,
        method: "PATCH",
        body,
      });
      return unwrapEnvelope(res, "folder", toFolder, "Failed to update folder");
    },

    moveFolder: async (
      _: unknown,
      {
        orgId,
        id,
        destinationParentId,
      }: {
        orgId: string;
        id: string;
        destinationParentId?: string | null;
      },
      ctx: GraphQLContext,
    ) => {
      assertAuthenticated(ctx);
      assertNotRootFolder(id, orgId, "move");

      await assertCan(ctx.userId, "write", "folder", id, orgId);
      const destId = destinationParentId ?? orgId;
      await assertCan(ctx.userId, "write", "folder", destId, orgId);

      const res = await assetFetch(
        assetPath(`${FOLDERS_PATH}/move`, { orgId, id }),
        {
          userId: ctx.userId,
          orgId,
          method: "PATCH",
          body: { destination_parent_id: destinationParentId ?? null },
        },
      );
      return unwrapEnvelope(res, "folder", toFolder, "Failed to move folder");
    },

    deleteFolder: async (
      _: unknown,
      {
        orgId,
        id,
      }: {
        orgId: string;
        id: string;
      },
      ctx: GraphQLContext,
    ) => {
      assertAuthenticated(ctx);
      assertNotRootFolder(id, orgId, "delete");

      await assertCan(ctx.userId, "delete", "folder", id, orgId);

      const res = await assetFetch(assetPath(FOLDERS_PATH, { orgId, id }), {
        userId: ctx.userId,
        orgId,
        method: "DELETE",
      });

      return unwrap204(res, "Failed to delete folder");
    },
  },
};
