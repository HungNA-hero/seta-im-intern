import { prisma } from "../prisma";

export async function assignRole(
  orgId: string,
  userId: string,
  roleId: string,
): Promise<void> {
  await prisma.user_roles.create({ data: { org_id: orgId, user_id: userId, role_id: roleId } });
}

export async function revokeRole(
  orgId: string,
  userId: string,
  roleId: string,
): Promise<void> {
  await prisma.user_roles.delete({ where: { org_id_user_id_role_id: { org_id: orgId, user_id: userId, role_id: roleId } } });
}
