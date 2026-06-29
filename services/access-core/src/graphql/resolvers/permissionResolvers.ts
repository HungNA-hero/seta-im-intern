import { listRolePermissions, RolePermission } from '../../db/queries/rolePermissions';
import { listObjectPermissions, ObjectPermission } from '../../db/queries/objectPermissions';
import { resource_type } from '@prisma/client';

export const permissionResolvers = {
  Query: {
    rolePermissions: async (_: unknown, { roleId }: { roleId: string }) => {
      const rows = await listRolePermissions(roleId);
      return rows.map((r: RolePermission) => ({
        id:           r.id,
        roleId:       r.roleId,
        actionId:     r.actionId,
        resourceType: r.resourceType,
      }));
    },

    objectPermissions: async (
      _: unknown,
      { orgId, resourceType, resourceId }: { orgId: string; resourceType: string; resourceId: string }
    ) => {
      const rows = await listObjectPermissions(orgId, resourceType as resource_type, resourceId);
      return rows.map((r: ObjectPermission) => ({
        id:            r.id,
        orgId:         r.orgId,
        resourceType:  r.resourceType,
        resourceId:    r.resourceId,
        granteeUserId: r.granteeUserId ?? null,
        granteeRoleId: r.granteeRoleId ?? null,
        actionId:      r.actionId,
        grantedBy:     r.grantedBy,
        grantedAt:     r.grantedAt.toISOString(),
      }));
    },
  },
};
