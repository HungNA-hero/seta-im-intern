import { listRolePermissions } from "../../db/queries/rolePermissions";
import {
  listObjectPermissions,
  grantObjectPermission,
  revokeObjectPermission,
} from "../../db/queries/objectPermissions";
import { PermissionActionCode, ResourceType } from "@prisma/client";
import { GraphQLError } from "graphql";
import { serializePermission, rethrowPrismaError } from "./utils";

export const permissionResolvers = {
  Query: {
    rolePermissions: async (_: unknown, { roleId }: { roleId: string }) =>
      listRolePermissions(roleId),
    objectPermissions: async (
      _: unknown,
      {
        orgId,
        resourceType,
        resourceId,
      }: { orgId: string; resourceType: ResourceType; resourceId: string },
    ) =>
      (await listObjectPermissions(orgId, resourceType, resourceId)).map(
        serializePermission,
      ),
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
        grantedBy,
      }: {
        orgId: string;
        resourceType: ResourceType;
        resourceId: string;
        action: PermissionActionCode;
        granteeUserId?: string | null;
        granteeRoleId?: string | null;
        grantedBy: string;
      },
    ) => {
      if (!!granteeUserId === !!granteeRoleId) {
        throw new GraphQLError(
          "Exactly one of granteeUserId or granteeRoleId must be set",
          {
            extensions: { code: "BAD_INPUT" },
          },
        );
      }
      try {
        return serializePermission(
          await grantObjectPermission(
            orgId,
            resourceType,
            resourceId,
            action,
            grantedBy,
            granteeUserId,
            granteeRoleId,
          ),
        );
      } catch (err) {
        rethrowPrismaError(err, {
          P2002: "Object permission already exists",
          P2025: "Permission action not found",
        });
      }
    },
    revokeObjectPermission: async (_: unknown, { id }: { id: string }) => {
      try {
        await revokeObjectPermission(id);
        return true;
      } catch (err) {
        rethrowPrismaError(err, { P2025: "Object permission not found" });
      }
    },
  },
};
