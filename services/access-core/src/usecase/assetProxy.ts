import { PermissionActionCode, ResourceType } from "@prisma/client";
import { assetFetch } from "../clients/assetClient";
import {
  assertAuthenticated,
  assertCan,
  GraphQLContext,
} from "../graphql/context";

export interface Precondition {
  action: PermissionActionCode;
  resourceType: ResourceType;
  resourceId: string;
}

export interface AuthorizedFetchInit {
  method?: "GET" | "POST" | "PATCH" | "DELETE";
  body?: Record<string, unknown>;
  includeOrgAdmin?: boolean;
}

export async function assertPreconditions(
  ctx: GraphQLContext,
  orgId: string,
  require: Precondition[],
): Promise<void> {
  assertAuthenticated(ctx);
  for (const precondition of require) {
    await assertCan(
      ctx.userId,
      precondition.action,
      precondition.resourceType,
      precondition.resourceId,
      orgId,
    );
  }
}

export async function authorizedFetch(
  ctx: GraphQLContext,
  orgId: string,
  require: Precondition[],
  path: string,
  init: AuthorizedFetchInit = {},
): Promise<Response> {
  assertAuthenticated(ctx);
  await assertPreconditions(ctx, orgId, require);
  return assetFetch(path, {
    userId: ctx.userId,
    orgId,
    orgAdmin: init.includeOrgAdmin && ctx.roles.includes("org_admin"),
    ...init,
  });
}
