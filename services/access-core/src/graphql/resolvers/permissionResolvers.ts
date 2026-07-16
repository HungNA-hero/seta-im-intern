import { listRolePermissions } from "../../db/queries/rolePermissions";
import {
  listObjectPermissions,
  grantObjectPermission,
  revokeObjectPermission,
  getObjectPermissionById,
} from "../../db/queries/objectPermissions";
import {
  isActiveOrgMember,
  roleBelongsToOrg,
} from "../../db/queries/organizations";
import { assertResourceInOrg } from "../../clients/resourceOrg";
import { PermissionActionCode, ResourceType } from "@prisma/client";
import { GraphQLError } from "graphql";
import { assertAuthenticated, assertCan, GraphQLContext } from "../context";
import { serializePermission, rethrowPrismaError } from "./utils";
import { getRoleById } from "../../db/queries/roles";

export const permissionResolvers = {
  Query: {
    rolePermissions: async (
      _: unknown,
      { roleId }: { roleId: string },
      ctx: GraphQLContext,
    ) => {
      const role = await getRoleById(roleId);
      if (!role || role.orgId !== ctx.currentOrgId) {
        throw new GraphQLError("Role not found", { extensions: { code: "BAD_USER_INPUT" } });
      }
      return listRolePermissions(roleId);
    },
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
      assertAuthenticated(ctx);
      if (!!granteeUserId === !!granteeRoleId) {
        throw new GraphQLError(
          "Exactly one of granteeUserId or granteeRoleId must be set",
          {
            extensions: { code: "GRANT_INVALID_TARGET" },
          },
        );
      }
      await assertCan(
        ctx.userId,
        "manage_permissions",
        resourceType,
        resourceId,
        orgId,
      );
      const granteeIsValid = granteeUserId
        ? isActiveOrgMember(orgId, granteeUserId).then((ok) => {
            if (!ok) {
              throw new GraphQLError(
                "Grantee is not an active member of this organization",
                { extensions: { code: "BAD_USER_INPUT" } },
              );
            }
          })
        : roleBelongsToOrg(orgId, granteeRoleId!).then((ok) => {
            if (!ok) {
              throw new GraphQLError(
                "Grantee role does not belong to this organization",
                { extensions: { code: "BAD_USER_INPUT" } },
              );
            }
          });

      await Promise.all([
        granteeIsValid,
        assertResourceInOrg(resourceType, resourceId, orgId, ctx.userId),
      ]);
      try {
        return serializePermission(
          await grantObjectPermission(
            orgId,
            resourceType,
            resourceId,
            action,
            ctx.userId,
            granteeUserId,
            granteeRoleId,
          ),
        );
      } catch (err) {
        rethrowPrismaError(err, {
          P2002: { message: "Object permission already exists", errorCode: "BAD_USER_INPUT" },
          P2025: { message: "Permission action not found", errorCode: "UNKNOWN_ACTION" },
        });
      }
    },
    revokeObjectPermission: async (
      _: unknown,
      { id }: { id: string },
      ctx: GraphQLContext,
    ) => {
      assertAuthenticated(ctx);
      const existing = await getObjectPermissionById(id);
      if (!existing) {
        throw new GraphQLError("Object permission not found", {
          extensions: { code: "GRANT_NOT_FOUND" },
        });
      }
      if (ctx.currentOrgId !== existing.orgId) {
        throw new GraphQLError("Forbidden", {
          extensions: { code: "FORBIDDEN" },
        });
      }
      await assertCan(
        ctx.userId,
        "manage_permissions",
        existing.resourceType as ResourceType,
        existing.resourceId,
        existing.orgId,
      );
      await revokeObjectPermission(id);
      return true;
    },
  },
};
