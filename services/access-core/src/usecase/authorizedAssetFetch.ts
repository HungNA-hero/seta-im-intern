import { PermissionActionCode, ResourceType } from "@prisma/client";
import { assetFetch } from "../clients/assetClient";
import { AssetTransport } from "../clients/assetTransport";
import { unauthenticated } from "../errors/factories";
import { assertCan } from "../graphql/context";

export interface AssetAuthorizationPrecondition {
  action: PermissionActionCode;
  resourceType: ResourceType;
  resourceId: string;
}

export interface AuthorizedAssetContext {
  userId: string | null;
  roles: string[];
}

export interface AuthorizedAssetFetchInit {
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  body?: Record<string, unknown>;
  idempotencyKey?: string;
  includeOrgAdmin?: boolean;
}

export interface AuthorizedAssetFetchRequest {
  ctx: AuthorizedAssetContext;
  orgId: string;
  path: string;
  preconditions?: AssetAuthorizationPrecondition[];
  init?: AuthorizedAssetFetchInit;
}

export interface AssetPreconditionRequest {
  ctx: AuthorizedAssetContext;
  orgId: string;
  preconditions: AssetAuthorizationPrecondition[];
}

export interface AuthorizedAssetFetchDependencies {
  authorization: {
    assertAllowed(input: {
      userId: string;
      action: PermissionActionCode;
      resourceType: ResourceType;
      resourceId: string;
      orgId: string;
    }): Promise<void>;
  };
  transport: AssetTransport;
}

export interface AuthorizedAssetFetch {
  authorizedFetch(request: AuthorizedAssetFetchRequest): Promise<Response>;
  assertPreconditions(request: AssetPreconditionRequest): Promise<void>;
}

export function createAuthorizedAssetFetch(dependencies: AuthorizedAssetFetchDependencies): AuthorizedAssetFetch {
  async function assertAllPreconditions(
    userId: string,
    orgId: string,
    preconditions: AssetAuthorizationPrecondition[],
  ): Promise<void> {
    for (const precondition of preconditions) {
      await dependencies.authorization.assertAllowed({
        userId,
        action: precondition.action,
        resourceType: precondition.resourceType,
        resourceId: precondition.resourceId,
        orgId,
      });
    }
  }

  return {
    async authorizedFetch({ ctx, orgId, path, preconditions = [], init = {} }): Promise<Response> {
      const userId = requireUserId(ctx);
      await assertAllPreconditions(userId, orgId, preconditions);

      return dependencies.transport.request(path, {
        userId,
        orgId,
        orgAdmin: init.includeOrgAdmin && ctx.roles.includes("org_admin"),
        method: init.method,
        body: init.body,
        idempotencyKey: init.idempotencyKey,
      });
    },

    async assertPreconditions({ ctx, orgId, preconditions }): Promise<void> {
      await assertAllPreconditions(requireUserId(ctx), orgId, preconditions);
    },
  };
}

function requireUserId(ctx: AuthorizedAssetContext): string {
  if (!ctx.userId) {
    throw unauthenticated();
  }
  return ctx.userId;
}

const authorizedAssetFetch = createAuthorizedAssetFetch({
  authorization: { assertAllowed: assertCan },
  transport: { request: assetFetch },
});

export const authorizedFetch = authorizedAssetFetch.authorizedFetch;
export const assertPreconditions = authorizedAssetFetch.assertPreconditions;
