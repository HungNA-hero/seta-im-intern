import { PermissionActionCode, ResourceType } from "@prisma/client";
import { getFolderMeta, getMetadataMeta } from "../clients/assetClient";
import { prisma } from "../db/prisma";
import {
  auditTrainerAdminDecision,
  getTrainerAdminGateState,
} from "./trainerAdmin";
import { ancestorIdsFromPath } from "../domain/ltreePath";
import { singleFlight } from "../cache/singleFlight";
import { readDecision, writeDecision } from "../cache/decisionCache";
import { decisionKey, hashRoleEpochs } from "../cache/keys";
import { getAssetEpoch, getRoleEpochs, getUserEpoch } from "../cache/epoch";

let permActionCachePromise: Promise<Map<string, string>> | null = null;

const rbacCeilingCache = new Map<string, boolean>();

function rbacCeilingCacheKey(
  roleIds: string[],
  permActionId: string,
  resourceType: ResourceType,
): string {
  return `${[...roleIds].sort().join(",")}::${permActionId}::${resourceType}`;
}

export function resetInProcessAuthzCachesForTests(): void {
  rbacCeilingCache.clear();
}

export interface PreloadedAuthContext {
  userId: string;
  orgId: string;
  roleCodes: string[];
  roleIds: string[];
  olpEnabled: boolean;
}

export interface CanDoOptions {
  preloaded?: PreloadedAuthContext;
  factMemo?: Map<string, Promise<unknown>>;
}

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
  preloaded?: PreloadedAuthContext;
  preResolved?: RoleResolution;
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

async function resolveRolesFromPreloaded(
  preloaded: PreloadedAuthContext,
  userId: string,
  action: PermissionActionCode,
): Promise<RoleResolution> {
  if (
    process.env.NODE_ENV !== "production" &&
    preloaded.roleCodes.includes("trainer_admin")
  ) {
    const state = getTrainerAdminGateState();
    auditTrainerAdminDecision(userId, state.enabled, state.reason);
    if (state.enabled) {
      return { allowed: true, reason: "trainer_admin" };
    }
  }

  if (preloaded.roleCodes.includes("org_admin")) {
    return { allowed: true, reason: "org_admin" };
  }

  const permActionId = await getPermActionId(action);
  if (!permActionId) {
    return { allowed: false, reason: "unknown action" };
  }

  return {
    roleIds: preloaded.roleIds,
    olpEnabled: preloaded.olpEnabled,
    permActionId,
  };
}

async function resolveRoles(
  userId: string,
  orgId: string,
  action: PermissionActionCode,
  preloaded?: PreloadedAuthContext,
): Promise<RoleResolution> {
  if (preloaded && preloaded.userId === userId && preloaded.orgId === orgId) {
    return resolveRolesFromPreloaded(preloaded, userId, action);
  }

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
  const cacheKey = rbacCeilingCacheKey(roleIds, permActionId, resourceType);
  const cached = rbacCeilingCache.get(cacheKey);
  if (cached !== undefined) return cached;

  const allowed = await prisma.rolePermission
    .findFirst({
      where: {
        roleId: { in: roleIds },
        actionId: permActionId,
        resourceType,
      },
    })
    .then(Boolean);
  rbacCeilingCache.set(cacheKey, allowed);
  return allowed;
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
  preloaded,
  preResolved,
}: DecideAllowedResourcesInput): Promise<AllowedResourcesDecision> {
  const resolution =
    preResolved ?? (await resolveRoles(userId, orgId, action, preloaded));
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
      if (
        ancestorIds.some((ancestorId) => grantedAncestorIds.has(ancestorId))
      ) {
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

function memoized<T>(
  memo: Map<string, Promise<unknown>> | undefined,
  key: string,
  fn: () => Promise<T>,
): Promise<T> {
  if (!memo) return fn();
  const existing = memo.get(key);
  if (existing) return existing as Promise<T>;
  const promise = fn();
  memo.set(key, promise);
  return promise;
}

function ancestorLoader(
  userId: string,
  orgId: string,
  resourceType: ResourceType,
  factMemo?: Map<string, Promise<unknown>>,
): ((resourceId: string) => Promise<string[]>) | undefined {
  if (resourceType === "folder") {
    return async (resourceId) => {
      const meta = await memoized(
        factMemo,
        `folder:${orgId}:${resourceId}`,
        () => getFolderMeta(orgId, userId, resourceId),
      );
      return meta ? ancestorIdsFromPath(meta.path) : [];
    };
  }
  if (resourceType === "metadata_item") {
    return async (resourceId) => {
      const meta = await memoized(factMemo, `item:${orgId}:${resourceId}`, () =>
        getMetadataMeta(orgId, userId, resourceId),
      );
      if (!meta) return [];
      const folderMeta = await memoized(
        factMemo,
        `folder:${orgId}:${meta.folderId}`,
        () => getFolderMeta(orgId, userId, meta.folderId),
      );
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
  options?: CanDoOptions,
): Promise<{ allowed: boolean; reason: string | null }> {
  if (!orgId) return { allowed: false, reason: "no org context" };

  const resolution = await resolveRoles(
    userId,
    orgId,
    action,
    options?.preloaded,
  );
  if (!("roleIds" in resolution)) {
    return { allowed: resolution.allowed, reason: resolution.reason };
  }

  const [assetEpoch, userEpoch, roleEpochs] = await Promise.all([
    getAssetEpoch(orgId),
    getUserEpoch(orgId, userId),
    getRoleEpochs(orgId, resolution.roleIds),
  ]);
  const key = decisionKey({
    orgId,
    assetEpoch,
    userEpoch,
    roleEpochsHash: hashRoleEpochs(roleEpochs),
    action,
    resourceType,
    resourceId,
  });

  const cached = await readDecision(key);
  if (cached) return cached;

  const compute = async (): Promise<{
    allowed: boolean;
    reason: string | null;
  }> => {
    const decision = await decideAllowedResources({
      userId,
      orgId,
      action,
      resourceType,
      resourceIds: [resourceId],
      getAncestorIds: ancestorLoader(
        userId,
        orgId,
        resourceType,
        options?.factMemo,
      ),
      rbacOnly: resourceType === "folder" && resourceId === orgId,
      preResolved: resolution,
    });
    return {
      allowed: decision.allowedIds.has(resourceId),
      reason: decision.reason,
    };
  };

  const result = await singleFlight(key, compute);
  await writeDecision(key, result);
  return result;
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
