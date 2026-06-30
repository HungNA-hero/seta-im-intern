import { GraphQLError } from "graphql";
import { prisma } from "../db/prisma";

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
