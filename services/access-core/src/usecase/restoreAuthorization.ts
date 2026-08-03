import { PermissionActionCode, ResourceType } from "@prisma/client";
import { getFolderRestoreAuthorizationFact, getMetadataRestoreAuthorizationFact } from "../clients/assetClient";
import { canDoWithKnownAncestors } from "../authz/decision";
import { ancestorIdsFromPath } from "../domain/ltreePath";
import { forbidden, resourceNotFound } from "../errors/factories";
import { assertOrgMember, GraphQLContext } from "../graphql/context";

const RESTORE_ACTIONS: PermissionActionCode[] = ["write", "delete"];

async function assertCurrentRestorePermission(
  ctx: GraphQLContext,
  orgId: string,
  resourceType: ResourceType,
  resourceId: string,
  ancestorIds: string[],
): Promise<void> {
  assertOrgMember(ctx);
  for (const action of RESTORE_ACTIONS) {
    const decision = await canDoWithKnownAncestors(ctx.userId, action, resourceType, resourceId, orgId, ancestorIds);
    if (decision.allowed) return;
  }
  throw forbidden("The requested restore is not permitted");
}

export async function authorizeFolderRestore(ctx: GraphQLContext, orgId: string, folderId: string): Promise<void> {
  assertOrgMember(ctx);
  const fact = await getFolderRestoreAuthorizationFact(orgId, ctx.userId, folderId);
  if (!fact) throw resourceNotFound("FOLDER_NOT_FOUND");
  await assertCurrentRestorePermission(ctx, orgId, "folder", fact.id, ancestorIdsFromPath(fact.path));
}

export async function authorizeMetadataRestore(ctx: GraphQLContext, orgId: string, metadataId: string): Promise<void> {
  assertOrgMember(ctx);
  const fact = await getMetadataRestoreAuthorizationFact(orgId, ctx.userId, metadataId);
  if (!fact) throw resourceNotFound("METADATA_NOT_FOUND");
  await assertCurrentRestorePermission(ctx, orgId, "metadata_item", fact.id, [
    fact.folderId,
    ...ancestorIdsFromPath(fact.folderPath),
  ]);
}
