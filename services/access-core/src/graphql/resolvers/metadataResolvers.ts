import { GraphQLError } from "graphql";
import { assertAuthenticated, assertCan, GraphQLContext } from "../context";
import { canDo } from "../../db/queries/canDo";
import {
  assetFetch,
  assetPath,
  snakeCaseKeys,
  unwrapEnvelope,
  unwrapListEnvelope,
  unwrap204,
} from "../../clients/assetClient";

const METADATA_PATH = "/internal/api/v1/metadata-items";

/** Represents one metadata item returned by the internal Go API. */
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

/** Contains optional filters accepted by the public metadata search query. */
interface MetadataSearchInput {
  folderId?: string | null;
  query?: string | null;
  labels?: string[] | null;
  category?: string | null;
  externalSource?: string | null;
  limit?: number | null;
  offset?: number | null;
}

interface NormalizedMetadataSearchInput {
  folderId?: string;
  query?: string;
  labels?: string[];
  category?: string;
  externalSource?: string;
  limit: number;
  offset: number;
}

/** Creates a consistently coded GraphQL validation error. */
function badUserInput(message: string): GraphQLError {
  return new GraphQLError(message, {
    extensions: { code: "BAD_USER_INPUT" },
  });
}

/**
 * Normalizes obvious search input before any Go request is made.
 * Go remains the authoritative validation boundary for the internal API.
 */
function normalizeMetadataSearchInput(
  input: MetadataSearchInput,
): NormalizedMetadataSearchInput {
  const normalized: NormalizedMetadataSearchInput = {
    limit: input.limit ?? 50,
    offset: input.offset ?? 0,
  };

  if (normalized.limit < 1 || normalized.limit > 100) {
    throw badUserInput("limit must be between 1 and 100");
  }
  if (normalized.offset < 0) {
    throw badUserInput("offset must be non-negative");
  }

  if (input.folderId !== undefined && input.folderId !== null) {
    const folderId = input.folderId.trim();
    if (folderId) normalized.folderId = folderId;
  }

  if (input.query !== undefined && input.query !== null) {
    const query = input.query.trim();
    const queryLength = [...query].length;
    if (queryLength < 2 || queryLength > 200) {
      throw badUserInput("query must contain between 2 and 200 characters");
    }
    normalized.query = query;
  }

  if (input.labels !== undefined && input.labels !== null) {
    const labels = input.labels.map((label) => label.trim());
    if (labels.some((label) => label.length === 0)) {
      throw badUserInput("labels must not contain blank values");
    }
    if (labels.length > 0) normalized.labels = [...new Set(labels)];
  }

  if (input.category !== undefined && input.category !== null) {
    const category = input.category.trim();
    if (category) normalized.category = category;
  }

  if (input.externalSource !== undefined && input.externalSource !== null) {
    const externalSource = input.externalSource.trim();
    if (externalSource) normalized.externalSource = externalSource;
  }

  if (
    !normalized.folderId &&
    !normalized.query &&
    !normalized.labels?.length &&
    !normalized.category &&
    !normalized.externalSource
  ) {
    throw badUserInput("at least one search filter must be provided");
  }

  return normalized;
}

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

/** Implements metadata GraphQL operations over the shared Asset Core client. */
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

    searchMetadata: async (
      _: unknown,
      {
        orgId,
        input,
      }: {
        orgId: string;
        input: MetadataSearchInput;
      },
      ctx: GraphQLContext,
    ) => {
      assertAuthenticated(ctx);

      const filters = normalizeMetadataSearchInput(input);

      const queryParams: Record<string, string | string[]> = { orgId };
      if (filters.folderId) queryParams.folderId = filters.folderId;
      if (filters.query) queryParams.query = filters.query;
      if (filters.labels) queryParams.label = filters.labels;
      if (filters.category) queryParams.category = filters.category;
      if (filters.externalSource)
        queryParams.externalSource = filters.externalSource;
      queryParams.limit = filters.limit.toString();
      queryParams.offset = filters.offset.toString();

      const resp = await assetFetch(
        assetPath(`${METADATA_PATH}/search`, queryParams),
        { userId: ctx.userId, orgId },
      );

      const items = await unwrapListEnvelope(
        resp,
        "items",
        (m: GoMetadataItem) => m,
        "Failed to search metadata items",
      );

      const result: ReturnType<typeof toMetadataItem>[] = [];
      for (const item of items) {
        const { allowed } = await canDo(
          ctx.userId,
          "read",
          "metadata_item",
          item.id,
          orgId,
        );
        if (allowed) {
          result.push(toMetadataItem(item));
        }
      }

      return result;
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

    deleteMetadata: async (
      _: unknown,
      { orgId, id }: { orgId: string; id: string },
      ctx: GraphQLContext,
    ) => {
      assertAuthenticated(ctx);
      await assertCan(ctx.userId, "delete", "metadata_item", id, orgId);

      const res = await assetFetch(assetPath(METADATA_PATH, { orgId, id }), {
        userId: ctx.userId,
        orgId,
        method: "DELETE",
      });

      return unwrap204(res, "Failed to delete metadata item");
    },
  },
};
