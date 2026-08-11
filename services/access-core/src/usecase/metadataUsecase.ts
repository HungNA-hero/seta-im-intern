import {
  assetPath,
  METADATA_PATH,
  snakeCaseKeys,
  unwrap204,
  unwrapEnvelope,
  unwrapListEnvelope,
} from "../clients/assetClient";
import { filterVisible } from "../authz/decision";
import {
  buildSearchQueryParams,
  CreateMetadataInput,
  GoMetadataItem,
  metadataHierarchy,
  MetadataConnectionSearchInput,
  MetadataSearchInput,
  toMetadataItem,
  UpdateMetadataInput,
} from "../domain/metadata";
import { badUserInput } from "../errors/factories";
import { assertAuthenticated, GraphQLContext } from "../graphql/context";
import {
  normalizeMetadataConnectionSearchInput,
  normalizeMetadataSearchInput,
  validateAndParseJsonString,
} from "../domain/metadataValidation";
import { assertPreconditions, authorizedFetch } from "./assetProxy";
import { loadFolderAncestorMap, loadFolderAncestors } from "./folderAncestors";
import { fetchMetadataCandidatePage } from "./metadataCandidatePage";
import { createVisibleMetadataPageReader } from "./visibleMetadataPage";
import { authorizeMetadataRestore } from "./restoreAuthorization";

const visibleMetadataPageReader = createVisibleMetadataPageReader({
  fetchCandidatePage: fetchMetadataCandidatePage,
  getFolderAncestors: loadFolderAncestors,
  filterVisible,
});

export async function listMetadataItems(ctx: GraphQLContext, orgId: string, folderId: string) {
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
  const folderAncestors = await loadFolderAncestorMap(
    orgId,
    ctx.userId,
    items.map((item) => item.folder_id),
  );
  const visible = await filterVisible({
    userId: ctx.userId,
    orgId,
    action: "read",
    resourceType: "metadata_item",
    items,
    getHierarchy: metadataHierarchy(folderAncestors),
  });
  return visible.map(toMetadataItem);
}

export async function getMetadataItem(ctx: GraphQLContext, orgId: string, id: string) {
  const response = await authorizedFetch(
    ctx,
    orgId,
    [{ action: "read", resourceType: "metadata_item", resourceId: id }],
    assetPath(METADATA_PATH, { orgId, id }),
  );
  if (response.status === 404) return null;
  return unwrapEnvelope(response, "item", toMetadataItem, "Failed to fetch metadata item");
}

export async function searchMetadata(ctx: GraphQLContext, orgId: string, input: MetadataSearchInput) {
  assertAuthenticated(ctx);
  const filters = normalizeMetadataSearchInput(input);
  const queryParams: Record<string, string | string[]> = {
    orgId,
    ...buildSearchQueryParams(filters),
    limit: filters.limit.toString(),
    offset: filters.offset.toString(),
  };
  const response = await authorizedFetch(ctx, orgId, [], assetPath(`${METADATA_PATH}/search`, queryParams));
  const items = await unwrapListEnvelope(
    response,
    "items",
    (item: GoMetadataItem) => item,
    "Failed to search metadata items",
  );
  const folderAncestors = await loadFolderAncestorMap(
    orgId,
    ctx.userId,
    items.map((item) => item.folder_id),
  );
  const visible = await filterVisible({
    userId: ctx.userId,
    orgId,
    action: "read",
    resourceType: "metadata_item",
    items,
    getHierarchy: metadataHierarchy(folderAncestors),
  });
  return visible.map(toMetadataItem);
}

export async function searchMetadataConnection(
  ctx: GraphQLContext,
  orgId: string,
  input: MetadataConnectionSearchInput,
) {
  assertAuthenticated(ctx);
  const filters = normalizeMetadataConnectionSearchInput(input);
  return visibleMetadataPageReader.readVisibleMetadataPage({ ctx, orgId, filters });
}

export async function createMetadata(ctx: GraphQLContext, orgId: string, input: CreateMetadataInput) {
  await assertPreconditions(ctx, orgId, [{ action: "write", resourceType: "folder", resourceId: input.folderId }]);
  const response = await authorizedFetch(ctx, orgId, [], assetPath(METADATA_PATH, { orgId }), {
    method: "POST",
    body: {
      ...snakeCaseKeys(input),
      metadata_json: validateAndParseJsonString(input.metadataJson) ?? {},
    },
  });
  return unwrapEnvelope(response, "item", toMetadataItem, "Failed to create metadata item");
}

export async function updateMetadata(ctx: GraphQLContext, orgId: string, id: string, input: UpdateMetadataInput) {
  assertAuthenticated(ctx);
  if (Object.keys(input).length === 0) {
    throw badUserInput("At least one field must be provided");
  }

  await assertPreconditions(ctx, orgId, [{ action: "write", resourceType: "metadata_item", resourceId: id }]);
  const body = snakeCaseKeys(input);
  if (input.metadataJson !== undefined) {
    body.metadata_json = validateAndParseJsonString(input.metadataJson);
  }
  const response = await authorizedFetch(ctx, orgId, [], assetPath(METADATA_PATH, { orgId, id }), {
    method: "PATCH",
    body,
  });
  return unwrapEnvelope(response, "item", toMetadataItem, "Failed to update metadata item");
}

export async function deleteMetadata(ctx: GraphQLContext, orgId: string, id: string) {
  const response = await authorizedFetch(
    ctx,
    orgId,
    [{ action: "delete", resourceType: "metadata_item", resourceId: id }],
    assetPath(METADATA_PATH, { orgId, id }),
    { method: "DELETE" },
  );
  return unwrap204(response, "Failed to delete metadata item");
}

export async function restoreMetadata(ctx: GraphQLContext, orgId: string, id: string) {
  await authorizeMetadataRestore(ctx, orgId, id);
  const response = await authorizedFetch(ctx, orgId, [], assetPath(`${METADATA_PATH}/restore`, { orgId, id }), {
    method: "POST",
  });
  return unwrapEnvelope(response, "item", toMetadataItem, "Failed to restore metadata item");
}
