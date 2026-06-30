import { prisma } from "../prisma";
import { PermissionActionCode, ResourceType } from "@prisma/client";

export type ObjectPermission = {
  id: string;
  orgId: string;
  resourceType: string;
  resourceId: string;
  granteeUserId: string | null;
  granteeRoleId: string | null;
  actionId: string;
  grantedBy: string;
  grantedAt: Date;
};

export async function listObjectPermissions(
  orgId: string,
  resourceType: ResourceType,
  resourceId: string,
): Promise<ObjectPermission[]> {
  return prisma.objectPermission.findMany({
    where: { orgId, resourceType, resourceId },
  });
}

export async function grantObjectPermission(
  orgId: string,
  resourceType: ResourceType,
  resourceId: string,
  action: PermissionActionCode,
  grantedBy: string,
  granteeUserId?: string | null,
  granteeRoleId?: string | null,
): Promise<ObjectPermission> {
  const permAction = await prisma.permissionAction.findUniqueOrThrow({
    where: { code: action },
  });
  return prisma.objectPermission.create({
    data: {
      orgId,
      resourceType,
      resourceId,
      actionId: permAction.id,
      grantedBy,
      granteeUserId: granteeUserId ?? null,
      granteeRoleId: granteeRoleId ?? null,
    },
  });
}

export async function revokeObjectPermission(id: string): Promise<void> {
  await prisma.objectPermission.delete({ where: { id } });
}
