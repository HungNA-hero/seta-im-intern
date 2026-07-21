import { PermissionActionCode, ResourceType } from "@prisma/client";
import { getFolderMeta, getMetadataMeta } from "../clients/assetClient";
import { prisma } from "../db/prisma";
import {
  auditTrainerAdminDecision,
  getTrainerAdminGateState,
} from "./trainerAdmin";
import { ancestorIdsFromPath } from "../domain/ltreePath";

let permActionCachePromise: Promise<Map<string, string>> | null = null;

type RoleResolution =
  | { allowed: boolean; reason: string | null }
  | { roleIds: string[]; olpEnabled: boolean; permActionId: string };

interface AllowedResourcesDecision {
  reason: string | null;
  allowedIds: Set<string>;
}

interface DecideAllowedResourcesInput {
  userId: string;
  orgId: string;
  action: PermissionActionCode;
  resourceType: ResourceType;
  resourceIds: string[];
  getAncestorIds?: (resourceId: string) => Promise<string[]> | string[];
  rbacOnly?: boolean;
}

interface ResourceHierarchy {
  ancestorIds?: string[];
}

async function getPermActionId(
  code: PermissionActionCode,
): Promise<string | null> {
  if (!permActionCachePromise) {
    permActionCachePromise = prisma.permissionAction
      .findMany({ select: { code: true, id: true } })
      .then((rows) => new Map(rows.map((row) => [row.code, row.id])))
      .catch((error) => {
        permActionCachePromise = null;
        throw error;
      });
  }
  return (await permActionCachePromise).get(code) ?? null;
}

async function resolveRoles(
  userId: string,
  orgId: string,
  action: PermissionActionCode,
): Promise<RoleResolution> {
  const user = await prisma.user.findUnique({
    where: { id: userId },
    include: {
      userRoles: {
        include: { role: { select: { code: true } } },
      },
    },
  });

  if (!user || !user.isActive) {
    return { allowed: false, reason: "user not found" };
  }

  if (
    process.env.NODE_ENV !== "production" &&
    user.userRoles.some((userRole) => userRole.role.code === "trainer_admin")
  ) {
    const state = getTrainerAdminGateState();
    auditTrainerAdminDecision(userId, state.enabled, state.reason);
    if (state.enabled) {
      return { allowed: true, reason: "trainer_admin" };
    }
  }

  const orgRoles = user.userRoles.filter(
    (userRole) => userRole.orgId === orgId,
  );
  if (orgRoles.some((userRole) => userRole.role.code === "org_admin")) {
    return { allowed: true, reason: "org_admin" };
  }

  const [org, permActionId] = await Promise.all([
    prisma.organization.findUnique({
      where: { id: orgId },
      select: { olpEnabled: true },
    }),
    getPermActionId(action),
  ]);
  if (!permActionId) {
    return { allowed: false, reason: "unknown action" };
  }

  return {
    roleIds: orgRoles.map((userRole) => userRole.roleId),
    olpEnabled: org?.olpEnabled ?? false,
    permActionId,
  };
}

async function hasRbacCeiling(
  roleIds: string[],
  permActionId: string,
  resourceType: ResourceType,
): Promise<boolean> {
  return prisma.rolePermission
    .findFirst({
      where: {
        roleId: { in: roleIds },
        actionId: permActionId,
        resourceType,
      },
    })
    .then(Boolean);
}

async function queryGrantedIds(
  userId: string,
  orgId: string,
  resourceType: ResourceType,
  resourceIds: string[],
  permActionId: string,
  roleIds: string[],
): Promise<Set<string>> {
  if (resourceIds.length === 0) return new Set();
  const rows = await prisma.objectPermission.findMany({
    where: {
      orgId,
      resourceType,
      actionId: permActionId,
      resourceId: { in: resourceIds },
      OR: [{ granteeUserId: userId }, { granteeRoleId: { in: roleIds } }],
    },
    select: { resourceId: true },
  });
  return new Set(rows.map((row) => row.resourceId));
}

async function decideAllowedResources({
  userId,
  orgId,
  action,
  resourceType,
  resourceIds,
  getAncestorIds,
  rbacOnly = false,
}: DecideAllowedResourcesInput): Promise<AllowedResourcesDecision> {
  const resolution = await resolveRoles(userId, orgId, action);
  if (!("roleIds" in resolution)) {
    return {
      reason: resolution.reason,
      allowedIds: resolution.allowed ? new Set(resourceIds) : new Set(),
    };
  }

  if (rbacOnly || !resolution.olpEnabled) {
    const allowed = await hasRbacCeiling(
      resolution.roleIds,
      resolution.permActionId,
      resourceType,
    );
    return {
      reason: allowed ? null : "no RBAC ceiling",
      allowedIds: allowed ? new Set(resourceIds) : new Set(),
    };
  }

  const allowedIds = await queryGrantedIds(
    userId,
    orgId,
    resourceType,
    resourceIds,
    resolution.permActionId,
    resolution.roleIds,
  );
  if (action !== "manage_permissions" && getAncestorIds) {
    const pendingIds = resourceIds.filter((id) => !allowedIds.has(id));
    const ancestorEntries = await Promise.all(
      pendingIds.map(async (id) => [id, await getAncestorIds(id)] as const),
    );
    const allAncestorIds = [
      ...new Set(ancestorEntries.flatMap(([, ancestorIds]) => ancestorIds)),
    ];
    const ancestorResourceType: ResourceType =
      resourceType === "metadata_item" ? "folder" : resourceType;
    const grantedAncestorIds = await queryGrantedIds(
      userId,
      orgId,
      ancestorResourceType,
      allAncestorIds,
      resolution.permActionId,
      resolution.roleIds,
    );
    for (const [id, ancestorIds] of ancestorEntries) {
      if (ancestorIds.some((ancestorId) => grantedAncestorIds.has(ancestorId))) {
        allowedIds.add(id);
      }
    }
  }

  return {
    reason: resourceIds.every((resourceId) => allowedIds.has(resourceId))
      ? null
      : "no object permission",
    allowedIds,
  };
}

function ancestorLoader(
  userId: string,
  orgId: string,
  resourceType: ResourceType,
): ((resourceId: string) => Promise<string[]>) | undefined {
  if (resourceType === "folder") {
    return async (resourceId) => {
      const meta = await getFolderMeta(orgId, userId, resourceId);
      return meta ? ancestorIdsFromPath(meta.path) : [];
    };
  }
  if (resourceType === "metadata_item") {
    return async (resourceId) => {
      const meta = await getMetadataMeta(orgId, userId, resourceId);
      if (!meta) return [];
      const folderMeta = await getFolderMeta(orgId, userId, meta.folderId);
      return [
        meta.folderId,
        ...(folderMeta ? ancestorIdsFromPath(folderMeta.path) : []),
      ];
    };
  }
  return undefined;
}

export async function canDo(
  userId: string,
  action: PermissionActionCode,
  resourceType: ResourceType,
  resourceId: string,
  orgId: string | null,
): Promise<{ allowed: boolean; reason: string | null }> {
  if (!orgId) return { allowed: false, reason: "no org context" };

  const decision = await decideAllowedResources({
    userId,
    orgId,
    action,
    resourceType,
    resourceIds: [resourceId],
    getAncestorIds: ancestorLoader(userId, orgId, resourceType),
    rbacOnly: resourceType === "folder" && resourceId === orgId,
  });
  return {
    allowed: decision.allowedIds.has(resourceId),
    reason: decision.reason,
  };
}

export async function filterAllowedResourceIds<T extends { id: string }>(
  userId: string,
  orgId: string,
  action: PermissionActionCode,
  resourceType: ResourceType,
  resourceIds: string[],
  items?: T[],
  getHierarchy?: (item: T) => ResourceHierarchy,
): Promise<Set<string>> {
  if (resourceIds.length === 0) return new Set();
  const byId = new Map(items?.map((item) => [item.id, item]) ?? []);
  const getAncestorIds =
    items && getHierarchy
      ? (id: string) => {
          const item = byId.get(id);
          return item ? (getHierarchy(item).ancestorIds ?? []) : [];
        }
      : undefined;
  const decision = await decideAllowedResources({
    userId,
    orgId,
    action,
    resourceType,
    resourceIds,
    getAncestorIds,
  });
  return decision.allowedIds;
}

export async function filterVisible<T extends { id: string }>(
  userId: string,
  orgId: string,
  action: PermissionActionCode,
  resourceType: ResourceType,
  items: T[],
  getHierarchy?: (item: T) => ResourceHierarchy,
): Promise<T[]> {
  const allowed = await filterAllowedResourceIds(
    userId,
    orgId,
    action,
    resourceType,
    items.map((item) => item.id),
    items,
    getHierarchy,
  );
  return items.filter((item) => allowed.has(item.id));
}
