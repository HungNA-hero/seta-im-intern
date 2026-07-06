import { prisma } from "../prisma";
import { PermissionActionCode, ResourceType } from "@prisma/client";
import { getFolderMeta, getMetadataMeta } from "../../clients/assetClient";
import { ancestorIdsFromPath } from "../../util/ltreePath";

let permActionCachePromise: Promise<Map<string, string>> | null = null;

async function getPermActionId(
  code: PermissionActionCode,
): Promise<string | null> {
  if (!permActionCachePromise) {
    permActionCachePromise = prisma.permissionAction
      .findMany({ select: { code: true, id: true } })
      .then((rows) => new Map(rows.map((r) => [r.code, r.id])))
      .catch((err) => {
        permActionCachePromise = null;
        throw err;
      });
  }
  return (await permActionCachePromise).get(code) ?? null;
}

type RoleResolution =
  | { allowed: boolean; reason: string | null }
  | { roleIds: string[]; olpEnabled: boolean; permActionId: string };

/**
 * Resolves the trainer_admin/org_admin bypasses shared by `canDo` and
 * `filterAllowedResourceIds` — everything needed before the caller runs
 * its own ceiling + object-permission grant queries via `resolveGrant`.
 */
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

  if (!user || !user.isActive)
    return { allowed: false, reason: "user not found" };

  if (user.userRoles.some((ur) => ur.role.code === "trainer_admin")) {
    return { allowed: true, reason: "trainer_admin" };
  }

  const orgRoles = user.userRoles.filter((ur) => ur.orgId === orgId);

  if (orgRoles.some((ur) => ur.role.code === "org_admin")) {
    return { allowed: true, reason: "org_admin" };
  }

  const roleIds = orgRoles.map((ur) => ur.roleId);

  const [org, permActionId] = await Promise.all([
    prisma.organization.findUnique({
      where: { id: orgId },
      select: { olpEnabled: true },
    }),
    getPermActionId(action),
  ]);

  if (!permActionId) return { allowed: false, reason: "unknown action" };

  return { roleIds, olpEnabled: org?.olpEnabled ?? false, permActionId };
}

type DecidedRoles = Extract<RoleResolution, { roleIds: string[] }>;

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
  return new Set(rows.map((r) => r.resourceId));
}

function decideAccess(
  resolution: DecidedRoles,
  rbacAllows: boolean,
  olpAllows: boolean,
): { allowed: boolean; reason: string | null } {
  if (resolution.olpEnabled) {
    return olpAllows
      ? { allowed: true, reason: null }
      : { allowed: false, reason: "no object permission" };
  }
  return rbacAllows
    ? { allowed: true, reason: null }
    : { allowed: false, reason: "no RBAC ceiling" };
}

async function resolveGrant(
  userId: string,
  orgId: string,
  resourceType: ResourceType,
  resourceIds: string[],
  resolution: DecidedRoles,
): Promise<{ rbacAllows: boolean; grantedIds: Set<string> }> {
  if (!resolution.olpEnabled) {
    const rbacAllows = await prisma.rolePermission
      .findFirst({
        where: {
          roleId: { in: resolution.roleIds },
          actionId: resolution.permActionId,
          resourceType,
        },
      })
      .then(Boolean);
    return { rbacAllows, grantedIds: new Set() };
  }

  const grantedIds = await queryGrantedIds(
    userId,
    orgId,
    resourceType,
    resourceIds,
    resolution.permActionId,
    resolution.roleIds,
  );
  return { rbacAllows: true, grantedIds };
}

/**
 * Evaluates whether a user can perform a specific action on a given resource.
 *
 * Creator status (`created_by`) confers no automatic permission — access to
 * a resource the user created is subject to the same RBAC ceiling and OLP
 * grant rules as any other resource. A grant on a folder also covers its
 * descendant folders (ltree ancestor chain) and any metadata item filed
 * under that folder or one of its descendants — except `manage_permissions`,
 * which never inherits: it lets the grantee create further grants, so
 * letting it cascade down a tree would silently hand out permission-
 * management authority over content the grantor never explicitly covered
 * (including content added after the grant was made). A `manage_permissions`
 * grant only ever applies to the exact resource it was granted on.
 *
 * A folder `resourceId` equal to `orgId` denotes the org root (used to
 * authorize top-level folder creation, which has no real folder object to
 * grant against) — this is decided by RBAC ceiling alone, in both RBAC and
 * OLP mode, since no object-level grant can ever target it.
 *
 * 1. RBAC mode (olpEnabled = false):
 *    Decided by the RBAC ceiling alone (role permits this action on this
 *    resource type). Object-level grants are irrelevant and never queried,
 *    and ancestor-folder metadata is never fetched from Asset Core.
 *
 * 2. OLP mode (olpEnabled = true):
 *    Decided by the object-level grant alone (the specific resource, or an
 *    ancestor folder it inherits from, was shared with them) — ceiling is
 *    ignored and never queried.
 *
 * @param userId - The ID of the user requesting access.
 * @param action - The permission action code (e.g., READ_FOLDER, WRITE_METADATA).
 * @param resourceType - The type of resource (e.g., FOLDER, METADATA_ITEM).
 * @param resourceId - The specific UUID of the resource being accessed.
 * @param orgId - The ID of the organization context.
 * @returns An object containing `allowed` boolean and a string `reason` if denied.
 */
export async function canDo(
  userId: string,
  action: PermissionActionCode,
  resourceType: ResourceType,
  resourceId: string,
  orgId: string | null,
): Promise<{ allowed: boolean; reason: string | null }> {
  if (!orgId) return { allowed: false, reason: "no org context" };

  const resolution = await resolveRoles(userId, orgId, action);
  if (!("roleIds" in resolution)) {
    return { allowed: resolution.allowed, reason: resolution.reason };
  }

  if (resourceType === "folder") {
    if (resourceId === orgId) {
      const rbacAllows = await prisma.rolePermission
        .findFirst({
          where: {
            roleId: { in: resolution.roleIds },
            actionId: resolution.permActionId,
            resourceType: "folder",
          },
        })
        .then(Boolean);
      return rbacAllows
        ? { allowed: true, reason: null }
        : { allowed: false, reason: "no RBAC ceiling" };
    }

    if (!resolution.olpEnabled) {
      const { rbacAllows } = await resolveGrant(
        userId,
        orgId,
        "folder",
        [resourceId],
        resolution,
      );
      return decideAccess(resolution, rbacAllows, false);
    }

    const meta =
      action === "manage_permissions"
        ? null
        : await getFolderMeta(orgId, userId, resourceId);
    const ancestorIds = meta ? ancestorIdsFromPath(meta.path) : [];
    const { grantedIds } = await resolveGrant(
      userId,
      orgId,
      "folder",
      [resourceId, ...ancestorIds],
      resolution,
    );
    const olpAllows = grantedIds.size > 0;
    return decideAccess(resolution, true, olpAllows);
  }

  if (resourceType === "metadata_item") {
    if (!resolution.olpEnabled) {
      const { rbacAllows } = await resolveGrant(
        userId,
        orgId,
        "metadata_item",
        [resourceId],
        resolution,
      );
      return decideAccess(resolution, rbacAllows, false);
    }

    const { grantedIds: directGrantedIds } = await resolveGrant(
      userId,
      orgId,
      "metadata_item",
      [resourceId],
      resolution,
    );

    let folderGrantedIds = new Set<string>();
    if (action !== "manage_permissions") {
      const meta = await getMetadataMeta(orgId, userId, resourceId);
      if (meta) {
        const folderMeta = await getFolderMeta(orgId, userId, meta.folderId);
        const folderAncestorIds = folderMeta
          ? ancestorIdsFromPath(folderMeta.path)
          : [];
        folderGrantedIds = await queryGrantedIds(
          userId,
          orgId,
          "folder",
          [meta.folderId, ...folderAncestorIds],
          resolution.permActionId,
          resolution.roleIds,
        );
      }
    }
    const olpAllows = directGrantedIds.size > 0 || folderGrantedIds.size > 0;
    return decideAccess(resolution, true, olpAllows);
  }

  if (!resolution.olpEnabled) {
    const { rbacAllows } = await resolveGrant(
      userId,
      orgId,
      resourceType,
      [resourceId],
      resolution,
    );
    return decideAccess(resolution, rbacAllows, false);
  }

  const { grantedIds } = await resolveGrant(
    userId,
    orgId,
    resourceType,
    [resourceId],
    resolution,
  );
  const olpAllows = grantedIds.has(resourceId);
  return decideAccess(resolution, true, olpAllows);
}

interface ResourceHierarchy {
  ancestorIds?: string[];
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

  const resolution = await resolveRoles(userId, orgId, action);
  if (!("roleIds" in resolution)) {
    return resolution.allowed ? new Set(resourceIds) : new Set();
  }

  if (!resolution.olpEnabled) {
    const { rbacAllows } = await resolveGrant(
      userId,
      orgId,
      resourceType,
      resourceIds,
      resolution,
    );
    return rbacAllows ? new Set(resourceIds) : new Set();
  }

  const allowed = new Set<string>();

  const { grantedIds } = await resolveGrant(
    userId,
    orgId,
    resourceType,
    resourceIds,
    resolution,
  );

  for (const id of resourceIds) {
    if (grantedIds.has(id)) allowed.add(id);
  }

  if (items && getHierarchy && action !== "manage_permissions") {
    const byId = new Map(items.map((item) => [item.id, item]));
    const ancestorIdsByItem = new Map<string, string[]>();
    const allAncestorIds = new Set<string>();
    for (const id of resourceIds) {
      if (allowed.has(id)) continue;
      const item = byId.get(id);
      const ancestorIds = item ? (getHierarchy(item).ancestorIds ?? []) : [];
      ancestorIdsByItem.set(id, ancestorIds);
      ancestorIds.forEach((a) => allAncestorIds.add(a));
    }
    if (allAncestorIds.size > 0) {
      const ancestorResourceType: ResourceType =
        resourceType === "metadata_item" ? "folder" : resourceType;
      const ancestorGrantedIds = await queryGrantedIds(
        userId,
        orgId,
        ancestorResourceType,
        [...allAncestorIds],
        resolution.permActionId,
        resolution.roleIds,
      );
      for (const [id, ancestorIds] of ancestorIdsByItem) {
        if (ancestorIds.some((a) => ancestorGrantedIds.has(a))) allowed.add(id);
      }
    }
  }

  return allowed;
}

/**
 * Filters a list of resource-bearing items down to those the user is
 * allowed to `action` on, in a single batched permission check.
 *
 * `getHierarchy` lets callers who already fetched folder-ancestry data
 * (folder `path`, metadata item `folder_id`/`folder_path`) pass it in so
 * batch checks get inheritance without extra fetches.
 */
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
    items.map((i) => i.id),
    items,
    getHierarchy,
  );
  return items.filter((i) => allowed.has(i.id));
}
