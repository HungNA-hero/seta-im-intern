import {
  CreateMetadataInput,
  MetadataConnectionSearchInput,
  MetadataSearchInput,
  UpdateMetadataInput,
} from "../../domain/metadata";
import {
  createMetadata,
  deleteMetadata,
  getMetadataItem,
  listMetadataItems,
  searchMetadata,
  searchMetadataConnection,
  updateMetadata,
} from "../../usecase/metadataUsecase";
import { GraphQLContext } from "../context";

export const metadataResolvers = {
  Query: {
    metadataItems: (
      _: unknown,
      { orgId, folderId }: { orgId: string; folderId: string },
      ctx: GraphQLContext,
    ) => listMetadataItems(ctx, orgId, folderId),

    metadataItem: (
      _: unknown,
      { orgId, id }: { orgId: string; id: string },
      ctx: GraphQLContext,
    ) => getMetadataItem(ctx, orgId, id),

    searchMetadata: (
      _: unknown,
      { orgId, input }: { orgId: string; input: MetadataSearchInput },
      ctx: GraphQLContext,
    ) => searchMetadata(ctx, orgId, input),

    searchMetadataConnection: (
      _: unknown,
      {
        orgId,
        input,
      }: { orgId: string; input: MetadataConnectionSearchInput },
      ctx: GraphQLContext,
    ) => searchMetadataConnection(ctx, orgId, input),
  },

  Mutation: {
    createMetadata: (
      _: unknown,
      { orgId, input }: { orgId: string; input: CreateMetadataInput },
      ctx: GraphQLContext,
    ) => createMetadata(ctx, orgId, input),

    updateMetadata: (
      _: unknown,
      {
        orgId,
        id,
        input,
      }: { orgId: string; id: string; input: UpdateMetadataInput },
      ctx: GraphQLContext,
    ) => updateMetadata(ctx, orgId, id, input),

    deleteMetadata: (
      _: unknown,
      { orgId, id }: { orgId: string; id: string },
      ctx: GraphQLContext,
    ) => deleteMetadata(ctx, orgId, id),
  },
};
