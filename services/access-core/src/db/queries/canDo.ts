import { prisma } from "../prisma";
import { PermissionActionCode, ResourceType } from "@prisma/client";

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
  | { decided: true; allowed: boolean; reason: string | null }
  | {
      decided: false;
      roleIds: string[];
      olpEnabled: boolean;
      permActionId: string;
    };

/**
 * Resolves the trainer_admin/org_admin bypasses shared by `canDo` and
 * `filterAllowedResourceIds` — everything needed before the caller can run
 * its own ceiling + object-permission grant queries in parallel.
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
    return { decided: true, allowed: false, reason: "user not found" };

  if (user.userRoles.some((ur) => ur.role.code === "trainer_admin")) {
    return { decided: true, allowed: true, reason: "trainer_admin" };
  }

  const orgRoles = user.userRoles.filter((ur) => ur.orgId === orgId);

  if (orgRoles.some((ur) => ur.role.code === "org_admin")) {
    return { decided: true, allowed: true, reason: "org_admin" };
  }

  const roleIds = orgRoles.map((ur) => ur.roleId);

  const [org, permActionId] = await Promise.all([
    prisma.organization.findUnique({
      where: { id: orgId },
      select: { olpEnabled: true },
    }),
    getPermActionId(action),
  ]);

  if (!permActionId)
    return { decided: true, allowed: false, reason: "unknown action" };

  return {
    decided: false,
    roleIds,
    olpEnabled: org?.olpEnabled ?? false,
    permActionId,
  };
}

/**
 * Evaluates whether a user can perform a specific action on a given resource.
 * This is the core authorization function that supports two modes depending on the Organization's settings:
 * Combined rule (after trainer_admin / org_admin bypasses):
 *   allowed = grantExists && (org.olpEnabled || ceilingExists)
 *
 * 1. RBAC mode (olpEnabled = false):
 *    Requires BOTH a ceiling (role permits this action on this resource type)
 *    AND an object-level grant (the specific resource was shared with them).
 *
 * 2. OLP mode (olpEnabled = true):
 *    Only the object-level grant matters — ceiling is ignored.
 *
 * Hierarchy of checks:
 * - System Admin (`trainer_admin`) -> Always Allowed
 * - Org Admin (`org_admin` in current Org) -> Always Allowed
 * - Regular Member -> ceiling checked first ("no RBAC ceiling"), then grant ("no object permission").
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
  if (resolution.decided) {
    return { allowed: resolution.allowed, reason: resolution.reason };
  }

  const [rbacAllows, olpAllows] = await Promise.all([
    prisma.rolePermission
      .findFirst({
        where: {
          roleId: { in: resolution.roleIds },
          actionId: resolution.permActionId,
          resourceType,
        },
      })
      .then(Boolean),
    prisma.objectPermission
      .findFirst({
        where: {
          orgId,
          resourceType,
          resourceId,
          actionId: resolution.permActionId,
          OR: [
            { granteeUserId: userId },
            { granteeRoleId: { in: resolution.roleIds } },
          ],
        },
      })
      .then(Boolean),
  ]);

  if (resolution.olpEnabled && olpAllows) {
    return { allowed: true, reason: null };
  }

  if (!rbacAllows) {
    return { allowed: false, reason: "no RBAC ceiling" };
  }

  return olpAllows
    ? { allowed: true, reason: null }
    : { allowed: false, reason: "no object permission" };
}

export async function filterAllowedResourceIds(
  userId: string,
  orgId: string,
  action: PermissionActionCode,
  resourceType: ResourceType,
  resourceIds: string[],
): Promise<Set<string>> {
  if (resourceIds.length === 0) return new Set();

  const resolution = await resolveRoles(userId, orgId, action);
  if (resolution.decided) {
    return resolution.allowed ? new Set(resourceIds) : new Set();
  }

  const [rbacAllows, olpAllowedResourceIds] = await Promise.all([
    prisma.rolePermission
      .findFirst({
        where: {
          roleId: { in: resolution.roleIds },
          actionId: resolution.permActionId,
          resourceType,
        },
      })
      .then(Boolean),
    prisma.objectPermission
      .findMany({
        where: {
          orgId,
          resourceType,
          actionId: resolution.permActionId,
          resourceId: { in: resourceIds },
          OR: [
            { granteeUserId: userId },
            { granteeRoleId: { in: resolution.roleIds } },
          ],
        },
        select: { resourceId: true },
      })
      .then((rows) => rows.map((r) => r.resourceId)),
  ]);

  if (!resolution.olpEnabled && !rbacAllows) return new Set();
  return new Set(olpAllowedResourceIds);
}

/**
 * Filters a list of resource-bearing items down to those the user is
 * allowed to `action` on, in a single batched permission check.
 */
export async function filterVisible<T extends { id: string }>(
  userId: string,
  orgId: string,
  action: PermissionActionCode,
  resourceType: ResourceType,
  items: T[],
): Promise<T[]> {
  const allowed = await filterAllowedResourceIds(
    userId,
    orgId,
    action,
    resourceType,
    items.map((i) => i.id),
  );
  return items.filter((i) => allowed.has(i.id));
}
