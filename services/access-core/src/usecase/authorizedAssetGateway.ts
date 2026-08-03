import { PermissionActionCode, ResourceType } from "@prisma/client";
import { AssetTransport } from "../clients/assetTransport";

export interface AssetAuthorizationPrecondition {
  action: PermissionActionCode;
  resourceType: ResourceType;
  resourceId: string;
}

export interface AssetGatewayContext {
  userId: string | null;
  roles: string[];
}

export interface AuthorizedAssetRequest {
  context: AssetGatewayContext;
  orgId: string;
  preconditions: AssetAuthorizationPrecondition[];
  path: string;
  init?: {
    method?: "GET" | "POST" | "PATCH" | "DELETE";
    body?: Record<string, unknown>;
    includeOrgAdmin?: boolean;
  };
}

export interface AssetAuthorizationChecker {
  assertAllowed(input: {
    userId: string;
    action: PermissionActionCode;
    resourceType: ResourceType;
    resourceId: string;
    orgId: string;
  }): Promise<void>;
}

export interface AuthorizedAssetGateway {
  assertPreconditions(input: {
    context: AssetGatewayContext;
    orgId: string;
    preconditions: AssetAuthorizationPrecondition[];
  }): Promise<void>;
  fetch(input: AuthorizedAssetRequest): Promise<Response>;
}

export interface AuthorizedAssetGatewayDependencies {
  authorization: AssetAuthorizationChecker;
  transport: AssetTransport;
  createError(code: string, message: string): Error;
}

function authenticatedUserId(
  context: AssetGatewayContext,
  createError: AuthorizedAssetGatewayDependencies["createError"],
): string {
  if (!context.userId) {
    throw createError("UNAUTHENTICATED", "Unauthenticated");
  }
  return context.userId;
}

export function createAuthorizedAssetGateway(dependencies: AuthorizedAssetGatewayDependencies): AuthorizedAssetGateway {
  async function assertPreconditions(input: {
    context: AssetGatewayContext;
    orgId: string;
    preconditions: AssetAuthorizationPrecondition[];
  }): Promise<void> {
    const userId = authenticatedUserId(input.context, dependencies.createError);
    for (const precondition of input.preconditions) {
      await dependencies.authorization.assertAllowed({
        userId,
        action: precondition.action,
        resourceType: precondition.resourceType,
        resourceId: precondition.resourceId,
        orgId: input.orgId,
      });
    }
  }

  return {
    assertPreconditions,
    async fetch(input): Promise<Response> {
      const userId = authenticatedUserId(input.context, dependencies.createError);
      await assertPreconditions(input);
      return dependencies.transport.request(input.path, {
        userId,
        orgId: input.orgId,
        orgAdmin: input.init?.includeOrgAdmin && input.context.roles.includes("org_admin"),
        method: input.init?.method,
        body: input.init?.body,
      });
    },
  };
}
