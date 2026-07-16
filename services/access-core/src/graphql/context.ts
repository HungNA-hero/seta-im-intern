import { GraphQLError } from "graphql";
import { PermissionActionCode, ResourceType } from "@prisma/client";
import { prisma } from "../db/prisma";
import { canDo } from "../db/queries/canDo";
import { assertTemporaryTrainerAdmin } from "../security/trainerAdmin";

export interface GraphQLContext {
  userId: string | null;
  currentOrgId: string | null;
  isMember: boolean;
  roles: string[];
  olpEnabled: boolean;
}

export function assertAuthenticated(
  ctx: GraphQLContext,
): asserts ctx is GraphQLContext & { userId: string } {
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
 * @param action The permission action code (e.g., "read", "write", "delete").
 * @param resourceType The type of resource being accessed (e.g., "folder", "metadata_item").
 * @param resourceId The ID of the resource being accessed.
 * @param orgId The ID of the organization context, if any.
 * @throws {GraphQLError} If the policy evaluation denies access.
 * Any unexpected exception from policy evaluation is propagated and masked by the server.
 */
export async function assertCan(
  userId: string,
  action: PermissionActionCode,
  resourceType: ResourceType,
  resourceId: string,
  orgId: string | null,
): Promise<void> {
  const { allowed, reason } = await canDo(
    userId,
    action,
    resourceType,
    resourceId,
    orgId,
  );
  if (!allowed) {
    throw new GraphQLError(reason ?? "Forbidden", {
      extensions: { code: "FORBIDDEN" },
    });
  }
}

export function assertOrgContext(ctx: GraphQLContext, orgId: string): void {
  if (ctx.currentOrgId !== orgId) {
    throw new GraphQLError(
      "Forbidden: orgId argument does not match the authenticated organization",
      { extensions: { code: "FORBIDDEN" } },
    );
  }
}

function emptyContext(): GraphQLContext {
  return {
    userId: null,
    currentOrgId: null,
    isMember: false,
    roles: [],
    olpEnabled: false,
  };
}

export async function loadRequestContext(
  userId: string | null,
  orgId: string | null,
): Promise<GraphQLContext> {
  if (!userId) return emptyContext();

  if (!orgId) {
    const user = await prisma.user.findUnique({ where: { id: userId } });
    if (!user || !user.isActive) return emptyContext();
    return {
      userId,
      currentOrgId: null,
      isMember: false,
      roles: [],
      olpEnabled: false,
    };
  }

  const [user, org] = await Promise.all([
    prisma.user.findUnique({
      where: { id: userId },
      include: {
        orgMembers: { where: { orgId } },
        userRoles: {
          where: { orgId },
          include: { role: { select: { code: true } } },
        },
      },
    }),
    prisma.organization.findUnique({
      where: { id: orgId },
      select: { olpEnabled: true },
    }),
  ]);

  if (!user || !user.isActive) return emptyContext();

  return {
    userId,
    currentOrgId: orgId,
    isMember: user.orgMembers.length > 0,
    roles: user.userRoles.map((ur) => ur.role.code),
    olpEnabled: org?.olpEnabled ?? false,
  };
}
