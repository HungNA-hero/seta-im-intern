import { listRolePermissions } from "../../db/queries/rolePermissions";
import { listObjectPermissions } from "../../db/queries/objectPermissions";
import { ResourceType } from "@prisma/client";
import { assertOrgMember, GraphQLContext } from "../context";

export const permissionResolvers = {
  Query: {
    rolePermissions: async (
      _: unknown,
      { roleId }: { roleId: string },
      ctx: GraphQLContext,
    ) => {
      assertOrgMember(ctx);
      return listRolePermissions(roleId);
    },

    objectPermissions: async (
      _: unknown,
      {
        orgId,
        resourceType,
        resourceId,
      }: { orgId: string; resourceType: ResourceType; resourceId: string },
      ctx: GraphQLContext,
    ) => {
      assertOrgMember(ctx);
      const rows = await listObjectPermissions(orgId, resourceType, resourceId);
      return rows.map((r) => ({ ...r, grantedAt: r.grantedAt.toISOString() }));
    },
  },
};
