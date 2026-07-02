import { GraphQLError } from "graphql";
import { assertAuthenticated, assertCan, GraphQLContext } from "../context";
import {
  assetFetch,
  assetPath,
  snakeCaseKeys,
  unwrapEnvelope,
  unwrapListEnvelope,
} from "../../clients/assetClient";

const METADATA_PATH = "/internal/api/v1/metadata-items";

interface GoMetadataItem {
  id: string;
  folder_id: string;
  title: string;
  description: string | null;
  labels: string[];
  category: string | null;
  external_source: string | null;
  external_id: string | null;
  source_url: string | null;
  thumbnail_url: string | null;
  license: string | null;
  author: string | null;
  metadata_json: Record<string, unknown>;
  notes: string | null;
  created_by: string;
  updated_by: string | null;
  created_at: string;
  updated_at: string;
}

interface CreateMetadataInput {
  folderId: string;
  title: string;
  description?: string | null;
  labels?: string[] | null;
  category?: string | null;
  externalSource?: string | null;
  externalId?: string | null;
  sourceUrl?: string | null;
  thumbnailUrl?: string | null;
  license?: string | null;
  author?: string | null;
  metadataJson?: string | null;
  notes?: string | null;
}

type UpdateMetadataInput = Omit<Partial<CreateMetadataInput>, "folderId">;

function toMetadataItem(m: GoMetadataItem) {
  return {
    id: m.id,
    folderId: m.folder_id,
    title: m.title,
    description: m.description,
    labels: m.labels || [],
    category: m.category,
    externalSource: m.external_source,
    externalId: m.external_id,
    sourceUrl: m.source_url,
    thumbnailUrl: m.thumbnail_url,
    license: m.license,
    author: m.author,
    metadataJson: JSON.stringify(m.metadata_json || {}),
    notes: m.notes,
    createdBy: m.created_by,
    updatedBy: m.updated_by,
    createdAt: m.created_at,
    updatedAt: m.updated_at,
  };
}

function validateAndParseJsonString(
  jsonString?: string | null,
): Record<string, unknown> | null | undefined {
  if (jsonString === undefined) return undefined;
  if (jsonString === null) return null;
  try {
    const parsed = JSON.parse(jsonString);
    if (
      typeof parsed !== "object" ||
      parsed === null ||
      Array.isArray(parsed)
    ) {
      throw new GraphQLError("metadataJson must be a JSON object string", {
        extensions: { code: "BAD_USER_INPUT" },
      });
    }
    return parsed as Record<string, unknown>;
  } catch (e) {
    if (e instanceof GraphQLError) throw e;
    throw new GraphQLError("metadataJson must be a valid JSON string", {
      extensions: { code: "BAD_USER_INPUT" },
    });
  }
}

export const metadataResolvers = {
  Query: {
    metadataItems: async (
      _: unknown,
      { orgId, folderId }: { orgId: string; folderId: string },
      ctx: GraphQLContext,
    ) => {
      assertAuthenticated(ctx);
      await assertCan(ctx.userId, "read", "folder", folderId, orgId);

      const resp = await assetFetch(
        assetPath(METADATA_PATH, { orgId, folderId }),
        { userId: ctx.userId, orgId },
      );

      return unwrapListEnvelope(
        resp,
        "items",
        toMetadataItem,
        "Failed to fetch metadata items",
      );
    },

    metadataItem: async (
      _: unknown,
      { orgId, id }: { orgId: string; id: string },
      ctx: GraphQLContext,
    ) => {
      assertAuthenticated(ctx);
      await assertCan(ctx.userId, "read", "metadata_item", id, orgId);

      const resp = await assetFetch(assetPath(METADATA_PATH, { orgId, id }), {
        userId: ctx.userId,
        orgId,
      });

      if (resp.status === 404) return null;
      return unwrapEnvelope(
        resp,
        "item",
        toMetadataItem,
        "Failed to fetch metadata item",
      );
    },
  },

  Mutation: {
    createMetadata: async (
      _: unknown,
      { orgId, input }: { orgId: string; input: CreateMetadataInput },
      ctx: GraphQLContext,
    ) => {
      assertAuthenticated(ctx);
      await assertCan(ctx.userId, "write", "folder", input.folderId, orgId);

      const body = snakeCaseKeys(input);
      // Create always sends an object so Go never needs to infer JSON null versus omission.
      body.metadata_json = validateAndParseJsonString(input.metadataJson) ?? {};

      const res = await assetFetch(assetPath(METADATA_PATH, { orgId }), {
        userId: ctx.userId,
        orgId,
        method: "POST",
        body,
      });
      return unwrapEnvelope(
        res,
        "item",
        toMetadataItem,
        "Failed to create metadata item",
      );
    },

    updateMetadata: async (
      _: unknown,
      {
        orgId,
        id,
        input,
      }: { orgId: string; id: string; input: UpdateMetadataInput },
      ctx: GraphQLContext,
    ) => {
      assertAuthenticated(ctx);

      if (Object.keys(input).length === 0) {
        throw new GraphQLError("At least one field must be provided", {
          extensions: { code: "BAD_USER_INPUT" },
        });
      }

      await assertCan(ctx.userId, "write", "metadata_item", id, orgId);

      const body = snakeCaseKeys(input);
      if (input.metadataJson !== undefined) {
        body.metadata_json = validateAndParseJsonString(input.metadataJson);
      }

      const res = await assetFetch(assetPath(METADATA_PATH, { orgId, id }), {
        userId: ctx.userId,
        orgId,
        method: "PATCH",
        body,
      });
      return unwrapEnvelope(
        res,
        "item",
        toMetadataItem,
        "Failed to update metadata item",
      );
    },
  },
};
