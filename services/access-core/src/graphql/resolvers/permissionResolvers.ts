import { listRolePermissions } from "../../db/queries/rolePermissions";
import {
  listObjectPermissions,
  grantObjectPermission,
  revokeObjectPermission,
  getObjectPermissionById,
} from "../../db/queries/objectPermissions";
import { PermissionActionCode, ResourceType } from "@prisma/client";
import { GraphQLError } from "graphql";
import { canDo } from "../../db/queries/canDo";
import { assertAuthenticated, GraphQLContext } from "../context";
import { serializePermission, rethrowPrismaError } from "./utils";

async function assertManagePermission(
  userId: string,
  orgId: string,
  resourceType: ResourceType,
  resourceId: string,
) {
  const { allowed, reason } = await canDo(
    userId,
    "manage_permissions",
    resourceType,
    resourceId,
    orgId,
  );
  if (!allowed) {
    throw new GraphQLError(reason ?? "Forbidden", {
      extensions: { code: "FORBIDDEN" },
    });
  }
}

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
      await assertManagePermission(ctx.userId, orgId, resourceType, resourceId);
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
      await assertManagePermission(
        ctx.userId,
        existing.orgId,
        existing.resourceType as ResourceType,
        existing.resourceId,
      );
      await revokeObjectPermission(id);
      return true;
    },
  },
};
