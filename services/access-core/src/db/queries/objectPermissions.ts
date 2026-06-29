import { prisma } from '../prisma';
import { resource_type } from '@prisma/client';

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

export async function listObjectPermissions(orgId: string, resourceType: string, resourceId: string): Promise<ObjectPermission[]> {
  const perms = await prisma.object_permissions.findMany({
    where: {
      org_id: orgId,
      resource_type: resourceType as resource_type,
      resource_id: resourceId,
    }
  });
  return perms.map(p => ({
    id: p.id,
    orgId: p.org_id,
    resourceType: p.resource_type,
    resourceId: p.resource_id,
    granteeUserId: p.grantee_user_id,
    granteeRoleId: p.grantee_role_id,
    actionId: p.action_id,
    grantedBy: p.granted_by,
    grantedAt: p.granted_at,
  }));
}
