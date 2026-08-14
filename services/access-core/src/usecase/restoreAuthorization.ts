import { ResourceType } from "@prisma/client";
import {
  getFolderRestoreAuthorizationFact,
  getLifecycleRestoreAuthorizationFact,
  getMetadataRestoreAuthorizationFact,
} from "../clients/assetClient";
import { GoLifecycleJob, LifecycleRootResourceType } from "../domain/lifecycleJob";
import { canDoWithKnownAncestors } from "../authz/decision";
import { ancestorIdsFromPath } from "../domain/ltreePath";
import { forbidden, resourceNotFound } from "../errors/factories";
import { assertOrgMember, GraphQLContext } from "../graphql/context";

async function assertCurrentRestorePermission(
  ctx: GraphQLContext,
  orgId: string,
  resourceType: ResourceType,
  resourceId: string,
  ancestorIds: string[],
): Promise<void> {
  assertOrgMember(ctx);
  const decision = await canDoWithKnownAncestors({
    userId: ctx.userId,
    action: "write",
    resourceType,
    resourceId,
    orgId,
    ancestorIds,
  });
  if (!decision.allowed) throw forbidden("The requested restore is not permitted");
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

interface LifecycleAuthorizationRoot {
  rootResourceType: LifecycleRootResourceType;
  rootResourceId: string;
  rootFolderId: string;
  rootFolderPath: string;
}

// authorizeLifecycleRestore resolves the trusted unit-to-root fact first.
// The browser supplies only unitId, so it cannot choose the resource whose
// write permission will be evaluated.
export async function authorizeLifecycleRestore(ctx: GraphQLContext, orgId: string, unitId: string): Promise<void> {
  assertOrgMember(ctx);
  const fact = await getLifecycleRestoreAuthorizationFact(orgId, ctx.userId, unitId);
  if (!fact) throw resourceNotFound("LIFECYCLE_UNIT_NOT_FOUND");
  await authorizeLifecycleRoot(ctx, orgId, fact);
}

// authorizeLifecycleJob applies the same current-write policy before job
// status becomes public. A job is not itself a permission-bearing resource.
export async function authorizeLifecycleJob(ctx: GraphQLContext, orgId: string, job: GoLifecycleJob): Promise<void> {
  await authorizeLifecycleRoot(ctx, orgId, {
    rootResourceType: job.root_resource_type,
    rootResourceId: job.root_resource_id,
    rootFolderId: job.root_folder_id,
    rootFolderPath: job.root_folder_path,
  });
}

async function authorizeLifecycleRoot(
  ctx: GraphQLContext,
  orgId: string,
  root: LifecycleAuthorizationRoot,
): Promise<void> {
  if (root.rootResourceType === "FOLDER") {
    await assertCurrentRestorePermission(
      ctx,
      orgId,
      "folder",
      root.rootResourceId,
      ancestorIdsFromPath(root.rootFolderPath),
    );
    return;
  }
  await assertCurrentRestorePermission(ctx, orgId, "metadata_item", root.rootResourceId, [
    root.rootFolderId,
    ...ancestorIdsFromPath(root.rootFolderPath),
  ]);
}
