import { prisma } from "../prisma";

export type RolePermission = {
  id: string;
  roleId: string;
  actionId: string;
  resourceType: string;
};

export async function listRolePermissions(
  roleId: string,
): Promise<RolePermission[]> {
  const perms = await prisma.rolePermission.findMany({ where: { roleId } });
  return perms.map((p) => ({
    id: p.id,
    roleId: p.roleId,
    actionId: p.actionId,
    resourceType: p.resourceType,
  }));
}
