import { prisma } from "../prisma";
import { ResourceType } from "@prisma/client";

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
  const perms = await prisma.objectPermission.findMany({
    where: { orgId, resourceType, resourceId },
  });
  return perms.map((p) => ({
    id: p.id,
    orgId: p.orgId,
    resourceType: p.resourceType,
    resourceId: p.resourceId,
    granteeUserId: p.granteeUserId,
    granteeRoleId: p.granteeRoleId,
    actionId: p.actionId,
    grantedBy: p.grantedBy,
    grantedAt: p.grantedAt,
  }));
}
