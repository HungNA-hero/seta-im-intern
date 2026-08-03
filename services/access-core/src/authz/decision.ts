import { PermissionActionCode, ResourceType } from "@prisma/client";
import { getAssetEpoch, getRoleEpochs, getUserEpoch } from "../cache/epoch";
import { readDecision, writeDecision } from "../cache/decisionCache";
import { singleFlight } from "../cache/singleFlight";
import { getFolderMeta, getMetadataMeta } from "../clients/assetClient";
import { prisma } from "../db/prisma";
import { createAuthorizationService } from "./authorizationService";
import { FilterAllowedResourcesRequest, FilterVisibleResourcesRequest } from "./contracts";
import { getAuthzRequestContext } from "./authzRequestContext";
import { auditTrainerAdminDecision, getTrainerAdminGateState } from "./trainerAdmin";

export type FilterAllowedResourceIdsInput<T extends { id: string } = { id: string }> = FilterAllowedResourcesRequest<T>;

export type FilterVisibleInput<T extends { id: string }> = FilterVisibleResourcesRequest<T>;

const authorizationService = createAuthorizationService({
  repository: {
    async findUser(userId) {
      const user = await prisma.user.findUnique({
        where: { id: userId },
        include: {
          userRoles: {
            include: { role: { select: { code: true } } },
          },
        },
      });
      if (!user) return null;
      return {
        isActive: user.isActive,
        roles: user.userRoles.map((userRole) => ({
          id: userRole.roleId,
          code: userRole.role.code,
          orgId: userRole.orgId,
        })),
      };
    },
    async getOlpEnabled(orgId) {
      return prisma.organization
        .findUnique({ where: { id: orgId }, select: { olpEnabled: true } })
        .then((organization) => organization?.olpEnabled ?? false);
    },
    async listPermissionActions() {
      return prisma.permissionAction.findMany({ select: { code: true, id: true } });
    },
    async hasRbacCeiling({ roleIds, permissionActionId, resourceType }) {
      return prisma.rolePermission
        .findFirst({
          where: {
            roleId: { in: roleIds },
            actionId: permissionActionId,
            resourceType,
          },
        })
        .then(Boolean);
    },
    async findGrantedResourceIds({ userId, orgId, resourceType, resourceIds, permissionActionId, roleIds }) {
      const rows = await prisma.objectPermission.findMany({
        where: {
          orgId,
          resourceType,
          actionId: permissionActionId,
          resourceId: { in: resourceIds },
          OR: [{ granteeUserId: userId }, { granteeRoleId: { in: roleIds } }],
        },
        select: { resourceId: true },
      });
      return rows.map((row) => row.resourceId);
    },
  },
  epochs: {
    getAssetEpoch,
    getUserEpoch,
    getRoleEpochs,
  },
  decisions: {
    read: readDecision,
    write: writeDecision,
  },
  hierarchy: {
    async getFolderPath(orgId, userId, folderId) {
      return (await getFolderMeta(orgId, userId, folderId))?.path ?? null;
    },
    async getMetadataFolderId(orgId, userId, metadataId) {
      return (await getMetadataMeta(orgId, userId, metadataId))?.folderId ?? null;
    },
  },
  trainerAdmin: {
    evaluate(userId, roleCodes) {
      if (process.env.NODE_ENV === "production" || !roleCodes.includes("trainer_admin")) {
        return null;
      }
      const state = getTrainerAdminGateState();
      auditTrainerAdminDecision(userId, state.enabled, state.reason);
      return state.enabled ? { allowed: true, reason: "trainer_admin" } : null;
    },
  },
  getRequestSnapshot: getAuthzRequestContext,
  runSingleFlight: singleFlight,
});

export function resetInProcessAuthzCachesForTests(): void {
  authorizationService.resetInProcessCaches();
}

export function canDo(
  userId: string,
  action: PermissionActionCode,
  resourceType: ResourceType,
  resourceId: string,
  orgId: string | null,
) {
  return authorizationService.canDo({
    userId,
    action,
    resourceType,
    resourceId,
    orgId,
  });
}

export function canDoWithKnownAncestors(
  userId: string,
  action: PermissionActionCode,
  resourceType: ResourceType,
  resourceId: string,
  orgId: string | null,
  ancestorIds: string[],
) {
  return authorizationService.canDoWithKnownAncestors({
    userId,
    action,
    resourceType,
    resourceId,
    orgId,
    ancestorIds,
  });
}

export function filterAllowedResourceIds<T extends { id: string }>(input: FilterAllowedResourceIdsInput<T>) {
  return authorizationService.filterAllowedResourceIds(input);
}

export function filterVisible<T extends { id: string }>(input: FilterVisibleInput<T>) {
  return authorizationService.filterVisible(input);
}
