import { prisma } from "../prisma";

export async function assignRole(
  orgId: string,
  userId: string,
  roleId: string,
): Promise<void> {
  await prisma.userRole.create({ data: { orgId, userId, roleId } });
}

export async function revokeRole(
  orgId: string,
  userId: string,
  roleId: string,
): Promise<void> {
  await prisma.userRole.delete({ where: { orgId_userId_roleId: { orgId, userId, roleId } } });
}
