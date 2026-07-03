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
            extensions: { code: "BAD_USER_INPUT" },
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
      if (granteeUserId && !(await isActiveOrgMember(orgId, granteeUserId))) {
        throw new GraphQLError(
          "Grantee is not an active member of this organization",
          { extensions: { code: "BAD_USER_INPUT" } },
        );
      }
      if (granteeRoleId && !(await roleBelongsToOrg(orgId, granteeRoleId))) {
        throw new GraphQLError(
          "Grantee role does not belong to this organization",
          { extensions: { code: "BAD_USER_INPUT" } },
        );
      }
      await assertResourceInOrg(resourceType, resourceId, orgId, ctx.userId);
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
          P2002: "Object permission already exists",
          P2025: "Permission action not found",
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
          extensions: { code: "NOT_FOUND" },
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
