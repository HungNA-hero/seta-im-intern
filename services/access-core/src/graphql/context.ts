import { GraphQLError } from "graphql";
import { PermissionActionCode, ResourceType } from "@prisma/client";
import { prisma } from "../db/prisma";
import { isOlpEnabled } from "../db/queries/authorization";
import { canDo } from "../authz/decision";
import { beginAuthorizationRequest } from "../authz/authzRequestContext";
import { assertTemporaryTrainerAdmin } from "../authz/trainerAdmin";
import { createRequestContextLoader, RequestContext } from "./requestContextLoader";

export interface GraphQLContext extends RequestContext {}

export interface CanDoInput {
  userId: string;
  action: PermissionActionCode;
  resourceType: ResourceType;
  resourceId: string;
  orgId: string | null;
}

export function assertAuthenticated(ctx: GraphQLContext): asserts ctx is GraphQLContext & { userId: string } {
  if (!ctx.userId) {
    throw new GraphQLError("Unauthenticated", {
      extensions: { code: "UNAUTHENTICATED" },
    });
  }
}

export function assertOrgMember(
  ctx: GraphQLContext,
): asserts ctx is GraphQLContext & { userId: string; currentOrgId: string } {
  assertAuthenticated(ctx);
  if (!ctx.isMember) {
    throw new GraphQLError("Forbidden: not a member of this organization", {
      extensions: { code: "FORBIDDEN" },
    });
  }
}

export function assertOrgAdmin(
  ctx: GraphQLContext,
): asserts ctx is GraphQLContext & { userId: string; currentOrgId: string } {
  assertOrgMember(ctx);
  if (!ctx.roles.includes("org_admin")) {
    throw new GraphQLError("Forbidden: organization administrator role required", {
      extensions: { code: "FORBIDDEN" },
    });
  }
}

export async function assertTrainerAdmin(ctx: GraphQLContext): Promise<void> {
  assertAuthenticated(ctx);
  try {
    await assertTemporaryTrainerAdmin(ctx.userId);
  } catch {
    throw new GraphQLError("Forbidden: temporary trainer administrator access required", {
      extensions: { code: "FORBIDDEN" },
    });
  }
}

/**
 * Evaluates the policy for a specific action on a resource and throws a GraphQLError if denied.
 * @param userId The ID of the user attempting the action.
 * @param action The permission action code (e.g., "read", "write", "manage_permissions").
 * @param resourceType The type of resource being accessed (e.g., "folder", "metadata_item").
 * @param resourceId The ID of the resource being accessed.
 * @param orgId The ID of the organization context, if any.
 * @throws {GraphQLError} If the policy evaluation denies access.
 * Any unexpected exception from policy evaluation is propagated and masked by the server.
 */
export async function assertCan(request: CanDoInput): Promise<void> {
  const { allowed, reason } = await canDo(request);
  if (!allowed) {
    throw new GraphQLError(reason ?? "Forbidden", {
      extensions: { code: "FORBIDDEN" },
    });
  }
}

export function assertOrgContext(ctx: GraphQLContext, orgId: string): void {
  if (ctx.currentOrgId !== orgId) {
    throw new GraphQLError("Forbidden: orgId argument does not match the authenticated organization", {
      extensions: { code: "FORBIDDEN" },
    });
  }
}

const requestContextLoader = createRequestContextLoader(
  {
    async findUser(userId, orgId) {
      const user = await prisma.user.findUnique({
        where: { id: userId },
        include: orgId
          ? {
              orgMembers: { where: { orgId } },
              userRoles: {
                include: { role: { select: { code: true } } },
              },
            }
          : undefined,
      });
      if (!user) return null;
      const scopedUser = user as typeof user & {
        orgMembers?: unknown[];
        userRoles?: Array<{ roleId: string; orgId: string; role: { code: string } }>;
      };
      return {
        isActive: user.isActive,
        isMember: (scopedUser.orgMembers?.length ?? 0) > 0,
        roles: (scopedUser.userRoles ?? []).map((userRole) => ({
          id: userRole.roleId,
          code: userRole.role.code,
          orgId: userRole.orgId,
        })),
      };
    },
    getOlpEnabled: isOlpEnabled,
  },
  { begin: beginAuthorizationRequest },
);

export async function loadRequestContext(userId: string | null, orgId: string | null): Promise<GraphQLContext> {
  return requestContextLoader.load(userId, orgId);
}
