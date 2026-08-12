import { assetPath, FOLDER_DELETIONS_PATH, unwrapEnvelope } from "../clients/assetClient";
import { toFolderDeletionJob, toFolderDeletionPreview } from "../domain/folderDeletion";
import { forbidden } from "../errors/factories";
import { assertAuthenticated, GraphQLContext } from "../graphql/context";
import { authorizedFetch } from "./authorizedAssetFetch";

function assertNotRootFolder(id: string, orgId: string): void {
  if (id === orgId) {
    throw forbidden("Cannot delete root folder");
  }
}

export async function previewFolderDeletion(ctx: GraphQLContext, orgId: string, folderId: string) {
  assertAuthenticated(ctx);
  assertNotRootFolder(folderId, orgId);
  const response = await authorizedFetch({
    ctx,
    orgId,
    preconditions: [{ action: "write", resourceType: "folder", resourceId: folderId }],
    path: assetPath(`${FOLDER_DELETIONS_PATH}/preview`, { orgId, folderId }),
    init: { method: "POST" },
  });
  return unwrapEnvelope(response, "preview", toFolderDeletionPreview, "Failed to preview folder deletion");
}

export async function confirmFolderDeletion(
  ctx: GraphQLContext,
  orgId: string,
  folderId: string,
  previewId: string,
  confirmationToken: string,
) {
  assertAuthenticated(ctx);
  assertNotRootFolder(folderId, orgId);
  const response = await authorizedFetch({
    ctx,
    orgId,
    preconditions: [{ action: "write", resourceType: "folder", resourceId: folderId }],
    path: assetPath(`${FOLDER_DELETIONS_PATH}/confirm`, { orgId, folderId }),
    init: {
      method: "POST",
      body: {
        preview_id: previewId,
        confirmation_token: confirmationToken,
      },
    },
  });
  return unwrapEnvelope(response, "job", toFolderDeletionJob, "Failed to confirm folder deletion");
}

async function folderDeletionJobRequest(ctx: GraphQLContext, orgId: string, id: string, action?: "cancel" | "retry") {
  const path = action ? `${FOLDER_DELETIONS_PATH}/jobs/${action}` : `${FOLDER_DELETIONS_PATH}/jobs`;
  const response = await authorizedFetch({
    ctx,
    orgId,
    path: assetPath(path, { orgId, id }),
    init: action ? { method: "POST", includeOrgAdmin: true } : { method: "GET", includeOrgAdmin: true },
  });
  return unwrapEnvelope(response, "job", toFolderDeletionJob, "Failed to load folder deletion job");
}

export function getFolderDeletionJob(ctx: GraphQLContext, orgId: string, id: string) {
  return folderDeletionJobRequest(ctx, orgId, id);
}

export function cancelFolderDeletionJob(ctx: GraphQLContext, orgId: string, id: string) {
  return folderDeletionJobRequest(ctx, orgId, id, "cancel");
}

export function retryFolderDeletionJob(ctx: GraphQLContext, orgId: string, id: string) {
  return folderDeletionJobRequest(ctx, orgId, id, "retry");
}
