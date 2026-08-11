import { assetPath, METADATA_PATH, throwAssetCoreError } from "../clients/assetClient";
import {
  buildSearchQueryParams,
  GoCursorSearchEnvelope,
  GoMetadataItem,
  isCursorCandidate,
  MetadataSearchFilters,
} from "../domain/metadata";
import { MetadataCursorPosition } from "../domain/metadataCursor";
import { internalError } from "../errors/factories";
import { GraphQLContext } from "../graphql/context";
import { authorizedFetch } from "./authorizedAssetFetch";

const INTERNAL_CURSOR_MODE = "true";

export interface MetadataCandidatePageRequest {
  ctx: GraphQLContext;
  orgId: string;
  filters: MetadataSearchFilters;
  batchSize: number;
  after?: MetadataCursorPosition;
}

export async function fetchMetadataCandidatePage({
  ctx,
  orgId,
  filters,
  batchSize,
  after,
}: MetadataCandidatePageRequest): Promise<GoCursorSearchEnvelope> {
  const queryParams: Record<string, string | string[]> = {
    orgId,
    ...buildSearchQueryParams(filters),
    cursor: INTERNAL_CURSOR_MODE,
    limit: batchSize.toString(),
  };
  if (after) {
    queryParams.afterUpdatedAt = after.updatedAt;
    queryParams.afterId = after.id;
  }

  const response = await authorizedFetch({
    ctx,
    orgId,
    path: assetPath(`${METADATA_PATH}/search`, queryParams),
  });
  return decodeCursorSearchEnvelope(response);
}

async function decodeCursorSearchEnvelope(response: Response): Promise<GoCursorSearchEnvelope> {
  if (!response.ok) await throwAssetCoreError(response);
  const data = (await response.json()) as Record<string, unknown>;
  if (!Array.isArray(data.items) || typeof data.hasMore !== "boolean" || !data.items.every(isCursorCandidate)) {
    throw internalError();
  }
  return { items: data.items as GoMetadataItem[], hasMore: data.hasMore };
}
