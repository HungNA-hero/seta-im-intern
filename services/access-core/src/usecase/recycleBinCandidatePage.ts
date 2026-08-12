import { assetPath, RECYCLE_BIN_PATH, throwAssetCoreError } from "../clients/assetClient";
import { GoRecycleBinEnvelope, isRecycleBinCandidate } from "../domain/recycleBin";
import { RecycleBinCursorPosition } from "../domain/recycleBinCursor";
import { internalError } from "../errors/factories";
import { GraphQLContext } from "../graphql/context";
import { authorizedFetch } from "./authorizedAssetFetch";

export interface RecycleBinCandidatePageRequest {
  ctx: GraphQLContext;
  orgId: string;
  batchSize: number;
  after?: RecycleBinCursorPosition;
}

export async function fetchRecycleBinCandidatePage({
  ctx,
  orgId,
  batchSize,
  after,
}: RecycleBinCandidatePageRequest): Promise<GoRecycleBinEnvelope> {
  const response = await authorizedFetch({
    ctx,
    orgId,
    path: assetPath(RECYCLE_BIN_PATH, {
      orgId,
      limit: batchSize.toString(),
      afterDeletedAt: after?.deletedAt,
      afterId: after?.lifecycleUnitId,
    }),
  });
  if (!response.ok) await throwAssetCoreError(response);

  const data = (await response.json()) as Record<string, unknown>;
  if (!Array.isArray(data.entries) || typeof data.hasMore !== "boolean" || !data.entries.every(isRecycleBinCandidate)) {
    throw internalError();
  }
  return { entries: data.entries, hasMore: data.hasMore };
}
