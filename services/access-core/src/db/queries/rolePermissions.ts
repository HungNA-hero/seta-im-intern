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
  const perms = await prisma.role_permissions.findMany({
    where: { role_id: roleId },
  });
  return perms.map((p) => ({
    id: p.id,
    roleId: p.role_id,
    actionId: p.action_id,
    resourceType: p.resource_type,
  }));
}
