import { prisma } from "../prisma";
import { PermissionActionCode, ResourceType } from "@prisma/client";
import { badUserInput, unknownAction } from "../../errors/factories";

export type ObjectPermission = {
  id: string;
  orgId: string;
  resourceType: ResourceType;
  resourceId: string;
  granteeUserId: string | null;
  granteeRoleId: string | null;
  actionId: string;
  grantedBy: string;
  grantedAt: Date;
};

export interface GrantObjectPermissionInput {
  orgId: string;
  resourceType: ResourceType;
  resourceId: string;
  action: PermissionActionCode;
  grantedBy: string;
  granteeUserId?: string | null;
  granteeRoleId?: string | null;
}

export async function listObjectPermissions(
  orgId: string,
  resourceType: ResourceType,
  resourceId: string,
): Promise<ObjectPermission[]> {
  return prisma.objectPermission.findMany({
    where: { orgId, resourceType, resourceId },
  });
}

export async function grantObjectPermission({
  orgId,
  resourceType,
  resourceId,
  action,
  grantedBy,
  granteeUserId,
  granteeRoleId,
}: GrantObjectPermissionInput): Promise<ObjectPermission> {
  const permissionAction = await prisma.permissionAction.findUnique({ where: { code: action } });
  if (!permissionAction) throw unknownAction();

  try {
    return await prisma.objectPermission.create({
      data: {
        orgId,
        resourceType,
        resourceId,
        actionId: permissionAction.id,
        grantedBy,
        granteeUserId: granteeUserId ?? null,
        granteeRoleId: granteeRoleId ?? null,
      },
    });
  } catch (error) {
    if ((error as { code?: unknown })?.code === "P2002") {
      throw badUserInput("Object permission already exists");
    }
    throw error;
  }
}

export async function getObjectPermissionById(id: string): Promise<ObjectPermission | null> {
  return prisma.objectPermission.findUnique({ where: { id } });
}

export async function revokeObjectPermission(id: string): Promise<void> {
  await prisma.objectPermission.delete({ where: { id } });
}
