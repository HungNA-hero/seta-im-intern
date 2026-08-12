import { listRolePermissions } from "../../db/queries/rolePermissions";
import { listObjectPermissions } from "../../db/queries/objectPermissions";
import { PermissionActionCode, ResourceType } from "@prisma/client";
import { GraphQLError } from "graphql";
import { GraphQLContext } from "../context";
import { serializePermission } from "./utils";
import { getRoleById } from "../../db/queries/roles";
import { grantPermission, revokePermission } from "../../usecase/objectPermissionService";

export const permissionResolvers = {
  Query: {
    rolePermissions: async (_: unknown, { roleId }: { roleId: string }, ctx: GraphQLContext) => {
      const role = await getRoleById(roleId);
      if (!role || role.orgId !== ctx.currentOrgId) {
        throw new GraphQLError("Role not found", { extensions: { code: "BAD_USER_INPUT" } });
      }
      return listRolePermissions(roleId);
    },
    objectPermissions: async (
      _: unknown,
      { orgId, resourceType, resourceId }: { orgId: string; resourceType: ResourceType; resourceId: string },
    ) => (await listObjectPermissions(orgId, resourceType, resourceId)).map(serializePermission),
  },
  Mutation: {
    grantObjectPermission: async (
      _: unknown,
      {
        orgId,
        resourceType,
        resourceId,
        action,
        granteeUserId,
        granteeRoleId,
      }: {
        orgId: string;
        resourceType: ResourceType;
        resourceId: string;
        action: PermissionActionCode;
        granteeUserId?: string | null;
        granteeRoleId?: string | null;
      },
      ctx: GraphQLContext,
    ) => {
      const permission = await grantPermission(ctx, {
        orgId,
        resourceType,
        resourceId,
        action,
        granteeUserId,
        granteeRoleId,
      });
      return serializePermission(permission);
    },
    revokeObjectPermission: async (_: unknown, { id }: { id: string }, ctx: GraphQLContext) => {
      await revokePermission(ctx, id);
      return true;
    },
  },
};
