import { prisma } from "../prisma";
import { PermissionActionCode, ResourceType } from "@prisma/client";

let permActionCachePromise: Promise<Map<string, string>> | null = null;

async function getPermActionId(
  code: PermissionActionCode,
): Promise<string | null> {
  if (!permActionCachePromise) {
    permActionCachePromise = prisma.permissionAction
      .findMany({ select: { code: true, id: true } })
      .then((rows) => new Map(rows.map((r) => [r.code, r.id])))
      .catch((err) => {
        permActionCachePromise = null;
        throw err;
      });
  }
  return (await permActionCachePromise).get(code) ?? null;
}

export async function canDo(
  userId: string,
  action: PermissionActionCode,
  resourceType: ResourceType,
  resourceId: string,
  orgId: string | null,
): Promise<{ allowed: boolean; reason: string | null }> {
  if (!orgId) return { allowed: false, reason: "no org context" };

  const user = await prisma.user.findUnique({
    where: { id: userId },
    include: {
      userRoles: {
        include: { role: { select: { code: true } } },
      },
    },
  });

  if (!user || !user.isActive)
    return { allowed: false, reason: "user not found" };

  if (user.userRoles.some((ur) => ur.role.code === "trainer_admin")) {
    return { allowed: true, reason: "trainer_admin" };
  }

  const orgRoles = user.userRoles.filter((ur) => ur.orgId === orgId);

  if (orgRoles.some((ur) => ur.role.code === "org_admin")) {
    return { allowed: true, reason: "org_admin" };
  }

  const roleIds = orgRoles.map((ur) => ur.roleId);

  const [org, permActionId] = await Promise.all([
    prisma.organization.findUnique({
      where: { id: orgId },
      select: { olpEnabled: true },
    }),
    getPermActionId(action),
  ]);

  if (!permActionId) return { allowed: false, reason: "unknown action" };

  if (!org?.olpEnabled) {
    const rbacCeiling = await prisma.rolePermission.findFirst({
      where: { roleId: { in: roleIds }, actionId: permActionId, resourceType },
    });
    return rbacCeiling
      ? { allowed: true, reason: null }
      : { allowed: false, reason: "no RBAC ceiling" };
  }

  const grant = await prisma.objectPermission.findFirst({
    where: {
      orgId,
      resourceType,
      resourceId,
      actionId: permActionId,
      OR: [{ granteeUserId: userId }, { granteeRoleId: { in: roleIds } }],
    },
  });
  return grant
    ? { allowed: true, reason: null }
    : { allowed: false, reason: "no object permission" };
}
