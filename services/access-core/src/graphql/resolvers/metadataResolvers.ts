import { GraphQLError } from "graphql";
import { canDo } from "../../db/queries/canDo";
import {
  assertAuthenticated,
  GraphQLContext,
} from "../context";
import { config } from "../../config";

/**
 * Interface representing the item structure returned by the Go internal API.
 */
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

/** GraphQL create input kept explicit so transport mapping cannot silently accept unknown resolver fields. */
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

/** GraphQL sparse update input preserves omitted fields separately from explicit null values. */
type UpdateMetadataInput = Omit<Partial<CreateMetadataInput>, "folderId">;

/**
 * Maps the internal snake_case Go object to the camelCase GraphQL object.
 * Converts labels to an array and metadata_json back to a JSON string.
 */
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

/** Maps internal Go transport failures to stable public GraphQL extension codes. */
const GO_ERROR_CODES: Record<number, string> = {
  400: "BAD_USER_INPUT",
  401: "UNAUTHENTICATED",
  403: "FORBIDDEN",
  404: "NOT_FOUND",
  409: "CONFLICT",
};

/**
 * Extracted helper to construct headers forwarded to Go.
 */
function goHeaders(userId: string, orgId: string): Record<string, string> {
  return { "X-User-Id": userId, "X-Org-Id": orgId };
}

/**
 * Extracted helper to map Go HTTP status to GraphQL extensions code.
 */
function throwGoError(res: Response, message: string): never {
  const code = GO_ERROR_CODES[res.status] ?? "INTERNAL_SERVER_ERROR";
  throw new GraphQLError(`${message}: ${res.statusText}`, {
    extensions: { code },
  });
}

/** Parses a successful Go mutation response and fails closed when its required item envelope is absent. */
async function handleGoItemResponse(
  response: Response,
  message: string,
): Promise<ReturnType<typeof toMetadataItem>> {
  if (!response.ok) throwGoError(response, message);
  const data = (await response.json()) as { item?: GoMetadataItem };
  if (!data.item) {
    throw new GraphQLError(`${message}: unexpected response format`, {
      extensions: { code: "INTERNAL_SERVER_ERROR" },
    });
  }
  return toMetadataItem(data.item);
}

/**
 * Asserts permissions by delegating to canDo. Throws FORBIDDEN on deny.
 * Enforces policy before forwarding request to the Go backend.
 */
async function assertMetadataPermission(
  userId: string,
  orgId: string,
  resourceId: string,
  resourceType: "folder" | "metadata_item",
  action: "read" | "write",
) {
  const { allowed, reason } = await canDo(
    userId,
    action,
    resourceType,
    resourceId,
    orgId,
  );
  if (!allowed) {
    throw new GraphQLError(reason ?? "Forbidden", {
      extensions: { code: "FORBIDDEN" },
    });
  }
}

/**
 * Validates JSON string input to ensure it is a valid object, not an array or scalar.
 * This guarantees that only valid objects (or null) reach the Go backend.
 */
function validateAndParseJsonString(
  jsonString?: string | null,
): Record<string, unknown> | null | undefined {
  if (jsonString === undefined) return undefined;
  if (jsonString === null) return null;
  try {
    const parsed = JSON.parse(jsonString);
    if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
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

/**
 * Maps incoming camelCase input arguments to snake_case for the Go API.
 */
function mapInputToSnakeCase(input: object): Record<string, unknown> {
  const mapped: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(input)) {
    const snakeKey = key.replace(
      /[A-Z]/g,
      (letter) => `_${letter.toLowerCase()}`,
    );
    mapped[snakeKey] = value;
  }
  return mapped;
}

/**
 * Resolvers for Metadata Queries and Mutations mapping directly to Go endpoints.
 */
export const metadataResolvers = {
  Query: {
    metadataItems: async (
      _: unknown,
      { orgId, folderId }: { orgId: string; folderId: string },
      ctx: GraphQLContext,
    ) => {
      assertAuthenticated(ctx);
      await assertMetadataPermission(ctx.userId, orgId, folderId, "folder", "read");

      const resp = await fetch(
        `${config.goAssetUrl}/internal/api/v1/metadata-items?orgId=${encodeURIComponent(orgId)}&folderId=${encodeURIComponent(folderId)}`,
        { headers: goHeaders(ctx.userId, orgId) },
      );

      if (!resp.ok) throwGoError(resp, "Failed to fetch metadata items");
      const data = (await resp.json()) as { items?: GoMetadataItem[] };
      if (!Array.isArray(data.items)) {
        throw new GraphQLError(
          "Failed to fetch metadata items: unexpected response format",
          { extensions: { code: "INTERNAL_SERVER_ERROR" } },
        );
      }
      return data.items.map(toMetadataItem);
    },

    metadataItem: async (
      _: unknown,
      { orgId, id }: { orgId: string; id: string },
      ctx: GraphQLContext,
    ) => {
      assertAuthenticated(ctx);
      await assertMetadataPermission(ctx.userId, orgId, id, "metadata_item", "read");

      const resp = await fetch(
        `${config.goAssetUrl}/internal/api/v1/metadata-items?orgId=${encodeURIComponent(orgId)}&id=${encodeURIComponent(id)}`,
        { headers: goHeaders(ctx.userId, orgId) },
      );

      if (resp.status === 404) return null;
      return handleGoItemResponse(resp, "Failed to fetch metadata item");
    },
  },

  Mutation: {
    createMetadata: async (
      _: unknown,
      { orgId, input }: { orgId: string; input: CreateMetadataInput },
      ctx: GraphQLContext,
    ) => {
      assertAuthenticated(ctx);
      await assertMetadataPermission(ctx.userId, orgId, input.folderId, "folder", "write");

      const body = mapInputToSnakeCase(input);
      // Create always sends an object so Go never needs to infer JSON null versus omission.
      body.metadata_json = validateAndParseJsonString(input.metadataJson) ?? {};

      const res = await fetch(
        `${config.goAssetUrl}/internal/api/v1/metadata-items?orgId=${encodeURIComponent(orgId)}`,
        {
          method: "POST",
          headers: {
            ...goHeaders(ctx.userId, orgId),
            "Content-Type": "application/json",
          },
          body: JSON.stringify(body),
        },
      );
      return handleGoItemResponse(res, "Failed to create metadata item");
    },

    updateMetadata: async (
      _: unknown,
      { orgId, id, input }: { orgId: string; id: string; input: UpdateMetadataInput },
      ctx: GraphQLContext,
    ) => {
      assertAuthenticated(ctx);

      if (Object.keys(input).length === 0) {
        throw new GraphQLError("At least one field must be provided", {
          extensions: { code: "BAD_USER_INPUT" },
        });
      }

      await assertMetadataPermission(ctx.userId, orgId, id, "metadata_item", "write");

      const body: Record<string, unknown> = {};
      for (const [key, value] of Object.entries(input)) {
        const snakeKey = key.replace(
          /[A-Z]/g,
          (letter) => `_${letter.toLowerCase()}`,
        );
        if (key === "metadataJson") {
          body[snakeKey] = validateAndParseJsonString(value as string | null);
        } else {
          body[snakeKey] = value;
        }
      }

      const res = await fetch(
        `${config.goAssetUrl}/internal/api/v1/metadata-items?orgId=${encodeURIComponent(orgId)}&id=${encodeURIComponent(id)}`,
        {
          method: "PATCH",
          headers: {
            ...goHeaders(ctx.userId, orgId),
            "Content-Type": "application/json",
          },
          body: JSON.stringify(body),
        },
      );
      return handleGoItemResponse(res, "Failed to update metadata item");
    },
  },
};
