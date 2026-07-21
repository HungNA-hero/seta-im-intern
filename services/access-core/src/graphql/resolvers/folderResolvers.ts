import { GraphQLResolveInfo } from "graphql";
import { FolderNode } from "../../domain/folder";
import {
  childrenOf,
  createFolder,
  deleteFolder,
  getFolder,
  listFolderChildren,
  listFolderTree,
  moveFolder,
  updateFolder,
} from "../../usecase/folderUsecase";
import { GraphQLContext } from "../context";
import { selectionIncludesField } from "../selection";

export const folderResolvers = {
  Query: {
    folder: (
      _: unknown,
      { orgId, id }: { orgId: string; id: string },
      ctx: GraphQLContext,
    ) => getFolder(ctx, orgId, id),

    folderTree: (
      _: unknown,
      { orgId, rootPath }: { orgId: string; rootPath?: string },
      ctx: GraphQLContext,
      info?: GraphQLResolveInfo,
    ) =>
      listFolderTree(
        ctx,
        orgId,
        rootPath,
        info === undefined || selectionIncludesField(info, "children"),
      ),

    folderChildren: (
      _: unknown,
      { orgId, parentPath }: { orgId: string; parentPath: string },
      ctx: GraphQLContext,
    ) => listFolderChildren(ctx, orgId, parentPath),
  },

  Folder: {
    children: (
      parent: FolderNode & { subtreeNodes?: FolderNode[] },
      _: unknown,
      ctx: GraphQLContext,
    ) => childrenOf(ctx, parent),
  },

  Mutation: {
    createFolder: (
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
    ) => createFolder(ctx, orgId, name, parentPath, description),

    updateFolder: (
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
    ) => updateFolder(ctx, orgId, id, name, description),

    moveFolder: (
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
    ) => moveFolder(ctx, orgId, id, destinationParentId),

    deleteFolder: (
      _: unknown,
      { orgId, id }: { orgId: string; id: string },
      ctx: GraphQLContext,
    ) => deleteFolder(ctx, orgId, id),
  },
};
