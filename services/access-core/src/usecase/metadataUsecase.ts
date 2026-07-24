import {
  assetPath,
  getFolderMetaBatch,
  METADATA_PATH,
  snakeCaseKeys,
  throwGoError,
  unwrap204,
  unwrapEnvelope,
  unwrapListEnvelope,
} from "../clients/assetClient";
import { filterVisible } from "../authz/decision";
import {
  CreateMetadataInput,
  GoCursorSearchEnvelope,
  GoMetadataItem,
  isCursorCandidate,
  metadataHierarchy,
  MetadataConnectionSearchInput,
  MetadataSearchInput,
  NormalizedMetadataSearchInput,
  toMetadataItem,
  UpdateMetadataInput,
} from "../domain/metadata";
import {
  encodeMetadataCursor,
  MetadataCursorPosition,
} from "../domain/metadataCursor";
import { badUserInput, internalError } from "../errors/factories";
import { assertAuthenticated, GraphQLContext } from "../graphql/context";
import { ancestorIdsFromPath } from "../domain/ltreePath";
import {
  normalizeMetadataConnectionSearchInput,
  normalizeMetadataSearchInput,
  validateAndParseJsonString,
} from "../domain/metadataValidation";
import { assertPreconditions, authorizedFetch } from "./assetProxy";

const CURSOR_CANDIDATE_LOOKAHEAD = 1;
const MAX_AUTHORIZATION_CANDIDATE_BATCHES = 10;
const INTERNAL_CURSOR_MODE = "true";

async function buildFolderAncestorMapForFolderIds(
  orgId: string,
  userId: string,
  folderIds: string[],
): Promise<Map<string, string[]>> {
  const uniqueIds = [...new Set(folderIds)];
  const metaById = await getFolderMetaBatch(orgId, userId, uniqueIds);
  return new Map(
    uniqueIds.map((folderId) => {
      const folderMeta = metaById.get(folderId);
      return [folderId, folderMeta ? ancestorIdsFromPath(folderMeta.path) : []];
    }),
  );
}

function buildSearchQueryParams(
  filters: Pick<
    NormalizedMetadataSearchInput,
    "folderId" | "query" | "labels" | "category" | "externalSource"
  >,
): Record<string, string | string[]> {
  const queryParams: Record<string, string | string[]> = {};
  if (filters.folderId) queryParams.folderId = filters.folderId;
  if (filters.query) queryParams.query = filters.query;
  if (filters.labels) queryParams.label = filters.labels;
  if (filters.category) queryParams.category = filters.category;
  if (filters.externalSource) {
    queryParams.externalSource = filters.externalSource;
  }
  return queryParams;
}

function toMetadataConnection(
  visible: GoMetadataItem[],
  requestedNodeCount: number,
  hasNextPage: boolean,
) {
  const nodes = visible.slice(0, requestedNodeCount);
  const lastNode = nodes[nodes.length - 1];
  return {
    nodes: nodes.map(toMetadataItem),
    pageInfo: {
      endCursor:
        lastNode === undefined
          ? null
          : encodeMetadataCursor({
              updatedAt: lastNode.updated_at,
              id: lastNode.id,
            }),
      hasNextPage,
    },
  };
}

async function unwrapCursorSearchEnvelope(
  response: Response,
): Promise<GoCursorSearchEnvelope> {
  if (!response.ok) await throwGoError(response);
  const data = (await response.json()) as Record<string, unknown>;
  if (
    !Array.isArray(data.items) ||
    typeof data.hasMore !== "boolean" ||
    !data.items.every(isCursorCandidate)
  ) {
    throw internalError();
  }
  return { items: data.items as GoMetadataItem[], hasMore: data.hasMore };
}

export async function listMetadataItems(
  ctx: GraphQLContext,
  orgId: string,
  folderId: string,
) {
  assertAuthenticated(ctx);
  const response = await authorizedFetch(
    ctx,
    orgId,
    [{ action: "read", resourceType: "folder", resourceId: folderId }],
    assetPath(METADATA_PATH, { orgId, folderId }),
  );
  const items = await unwrapListEnvelope(
    response,
    "items",
    (item: GoMetadataItem) => item,
    "Failed to fetch metadata items",
  );
  const folderAncestors = await buildFolderAncestorMapForFolderIds(
    orgId,
    ctx.userId,
    items.map((item) => item.folder_id),
  );
  const visible = await filterVisible(
    ctx.userId,
    orgId,
    "read",
    "metadata_item",
    items,
    metadataHierarchy(folderAncestors),
  );
  return visible.map(toMetadataItem);
}

export async function getMetadataItem(
  ctx: GraphQLContext,
  orgId: string,
  id: string,
) {
  const response = await authorizedFetch(
    ctx,
    orgId,
    [{ action: "read", resourceType: "metadata_item", resourceId: id }],
    assetPath(METADATA_PATH, { orgId, id }),
  );
  if (response.status === 404) return null;
  return unwrapEnvelope(
    response,
    "item",
    toMetadataItem,
    "Failed to fetch metadata item",
  );
}

export async function searchMetadata(
  ctx: GraphQLContext,
  orgId: string,
  input: MetadataSearchInput,
) {
  assertAuthenticated(ctx);
  const filters = normalizeMetadataSearchInput(input);
  const queryParams: Record<string, string | string[]> = {
    orgId,
    ...buildSearchQueryParams(filters),
    limit: filters.limit.toString(),
    offset: filters.offset.toString(),
  };
  const response = await authorizedFetch(
    ctx,
    orgId,
    [],
    assetPath(`${METADATA_PATH}/search`, queryParams),
  );
  const items = await unwrapListEnvelope(
    response,
    "items",
    (item: GoMetadataItem) => item,
    "Failed to search metadata items",
  );
  const folderAncestors = await buildFolderAncestorMapForFolderIds(
    orgId,
    ctx.userId,
    items.map((item) => item.folder_id),
  );
  const visible = await filterVisible(
    ctx.userId,
    orgId,
    "read",
    "metadata_item",
    items,
    metadataHierarchy(folderAncestors),
  );
  return visible.map(toMetadataItem);
}

export async function searchMetadataConnection(
  ctx: GraphQLContext,
  orgId: string,
  input: MetadataConnectionSearchInput,
) {
  assertAuthenticated(ctx);
  const filters = normalizeMetadataConnectionSearchInput(input);
  const candidateBatchSize = filters.first + CURSOR_CANDIDATE_LOOKAHEAD;
  const visible: GoMetadataItem[] = [];
  let scanAfter: MetadataCursorPosition | undefined = filters.after;
  const folderAncestors = await buildFolderAncestorMapForFolderIds(
    orgId,
    ctx.userId,
    [filters.folderId],
  );

  for (
    let batch = 0;
    batch < MAX_AUTHORIZATION_CANDIDATE_BATCHES;
    batch += 1
  ) {
    const queryParams: Record<string, string | string[]> = {
      orgId,
      ...buildSearchQueryParams(filters),
      cursor: INTERNAL_CURSOR_MODE,
      limit: candidateBatchSize.toString(),
    };
    if (scanAfter) {
      queryParams.afterUpdatedAt = scanAfter.updatedAt;
      queryParams.afterId = scanAfter.id;
    }

    const response = await authorizedFetch(
      ctx,
      orgId,
      [],
      assetPath(`${METADATA_PATH}/search`, queryParams),
    );
    const candidatePage = await unwrapCursorSearchEnvelope(response);
    if (candidatePage.items.length === 0 && candidatePage.hasMore) {
      throw internalError();
    }

    const authorized = await filterVisible(
      ctx.userId,
      orgId,
      "read",
      "metadata_item",
      candidatePage.items,
      metadataHierarchy(folderAncestors),
    );
    visible.push(...authorized);

    if (visible.length >= candidateBatchSize) {
      return toMetadataConnection(visible, filters.first, true);
    }
    if (!candidatePage.hasMore) {
      return toMetadataConnection(visible, filters.first, false);
    }

    const lastCandidate = candidatePage.items[candidatePage.items.length - 1];
    scanAfter = {
      updatedAt: lastCandidate.updated_at,
      id: lastCandidate.id,
    };
  }

  throw internalError();
}

export async function createMetadata(
  ctx: GraphQLContext,
  orgId: string,
  input: CreateMetadataInput,
) {
  await assertPreconditions(ctx, orgId, [
    { action: "write", resourceType: "folder", resourceId: input.folderId },
  ]);
  const response = await authorizedFetch(
    ctx,
    orgId,
    [],
    assetPath(METADATA_PATH, { orgId }),
    {
      method: "POST",
      body: {
        ...snakeCaseKeys(input),
        metadata_json: validateAndParseJsonString(input.metadataJson) ?? {},
      },
    },
  );
  return unwrapEnvelope(
    response,
    "item",
    toMetadataItem,
    "Failed to create metadata item",
  );
}

export async function updateMetadata(
  ctx: GraphQLContext,
  orgId: string,
  id: string,
  input: UpdateMetadataInput,
) {
  assertAuthenticated(ctx);
  if (Object.keys(input).length === 0) {
    throw badUserInput("At least one field must be provided");
  }

  await assertPreconditions(ctx, orgId, [
    { action: "write", resourceType: "metadata_item", resourceId: id },
  ]);
  const body = snakeCaseKeys(input);
  if (input.metadataJson !== undefined) {
    body.metadata_json = validateAndParseJsonString(input.metadataJson);
  }
  const response = await authorizedFetch(
    ctx,
    orgId,
    [],
    assetPath(METADATA_PATH, { orgId, id }),
    { method: "PATCH", body },
  );
  return unwrapEnvelope(
    response,
    "item",
    toMetadataItem,
    "Failed to update metadata item",
  );
}

export async function deleteMetadata(
  ctx: GraphQLContext,
  orgId: string,
  id: string,
) {
  const response = await authorizedFetch(
    ctx,
    orgId,
    [{ action: "delete", resourceType: "metadata_item", resourceId: id }],
    assetPath(METADATA_PATH, { orgId, id }),
    { method: "DELETE" },
  );
  return unwrap204(response, "Failed to delete metadata item");
}
