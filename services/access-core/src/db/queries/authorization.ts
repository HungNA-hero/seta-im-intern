import { prisma } from "../prisma";
import { AuthorizationUser, GrantedResourceQuery, RbacCeilingQuery } from "../../authz/contracts";

export async function findAuthorizationUser(userId: string): Promise<AuthorizationUser | null> {
  const user = await prisma.user.findUnique({
    where: { id: userId },
    include: {
      userRoles: {
        include: { role: { select: { code: true } } },
      },
    },
  });
  if (!user) return null;
  return {
    isActive: user.isActive,
    roles: user.userRoles.map((userRole) => ({
      id: userRole.roleId,
      code: userRole.role.code,
      orgId: userRole.orgId,
    })),
  };
}

export async function isOlpEnabled(orgId: string): Promise<boolean> {
  const organization = await prisma.organization.findUnique({
    where: { id: orgId },
    select: { olpEnabled: true },
  });
  return organization?.olpEnabled ?? false;
}

export async function listPermissionActions(): Promise<Array<{ code: string; id: string }>> {
  return prisma.permissionAction.findMany({ select: { code: true, id: true } });
}

export async function hasRbacCeiling({
  roleIds,
  permissionActionIds,
  resourceType,
}: RbacCeilingQuery): Promise<boolean> {
  const rolePermission = await prisma.rolePermission.findFirst({
    where: {
      roleId: { in: roleIds },
      actionId: { in: permissionActionIds },
      resourceType,
    },
  });
  return Boolean(rolePermission);
}

export async function findGrantedResourceIds({
  userId,
  orgId,
  resourceType,
  resourceIds,
  permissionActionIds,
  roleIds,
}: GrantedResourceQuery): Promise<string[]> {
  const rows = await prisma.objectPermission.findMany({
    where: {
      orgId,
      resourceType,
      actionId: { in: permissionActionIds },
      resourceId: { in: resourceIds },
      OR: [{ granteeUserId: userId }, { granteeRoleId: { in: roleIds } }],
    },
    select: { resourceId: true },
  });
  return rows.map((row) => row.resourceId);
}
