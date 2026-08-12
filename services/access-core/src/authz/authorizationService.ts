import { PermissionActionCode, ResourceType } from "@prisma/client";
import { decisionKey, hashRoleEpochs } from "../cache/keys";
import { memoize } from "../cache/singleFlight";
import { ancestorIdsFromPath } from "../domain/ltreePath";
import {
  AuthorizationDecision,
  AuthorizationService,
  AuthorizationServiceDependencies,
  CanDoRequest,
  FilterAllowedResourcesRequest,
  FilterVisibleResourcesRequest,
  KnownAncestorCanDoRequest,
} from "./contracts";

type RoleResolution = AuthorizationDecision | { roleIds: string[]; olpEnabled: boolean; permissionActionId: string };

const DIRECT_ACTION_CODES: Record<PermissionActionCode, PermissionActionCode[]> = {
  read: ["read", "write", "manage_permissions"],
  write: ["write", "manage_permissions"],
  // `delete` is retained only for internal compatibility with the historical
  // enum. The public model authorizes deletion through write.
  delete: ["write", "manage_permissions"],
  manage_permissions: ["manage_permissions"],
};

// `manage_permissions` is deliberately absent from inherited action sets.
// A direct manage grant controls only that exact resource; it must never become
// permission-management authority over every descendant folder or metadata item.
const INHERITED_ACTION_CODES: Record<PermissionActionCode, PermissionActionCode[]> = {
  read: ["read", "write"],
  write: ["write"],
  delete: ["write"],
  manage_permissions: [],
};

interface AllowedResourcesDecision {
  reason: string | null;
  allowedIds: Set<string>;
}

type AncestorIdLoader = ((resourceId: string) => Promise<string[]> | string[]) | undefined;

export function createAuthorizationService(dependencies: AuthorizationServiceDependencies): AuthorizationService {
  let permissionActionCache: Promise<Map<string, string>> | null = null;
  const rbacCeilingCache = new Map<string, boolean>();

  async function getPermissionActionId(code: PermissionActionCode): Promise<string | null> {
    if (!permissionActionCache) {
      permissionActionCache = dependencies.repository
        .listPermissionActions()
        .then((rows) => new Map(rows.map((row) => [row.code, row.id])))
        .catch((error) => {
          permissionActionCache = null;
          throw error;
        });
    }
    return (await permissionActionCache).get(code) ?? null;
  }

  async function resolveRoles(userId: string, orgId: string, action: PermissionActionCode): Promise<RoleResolution> {
    const snapshot = dependencies.getRequestSnapshot();
    if (snapshot?.userId === userId && snapshot.orgId === orgId) {
      return decideRoleResolution({
        userId,
        trainerAdminRoleCodes: snapshot.globalRoleCodes,
        orgAdminRoleCodes: snapshot.orgRoleCodes,
        roleIds: snapshot.roleIds,
        loadOlpEnabled: () => Promise.resolve(snapshot.olpEnabled),
        action,
      });
    }

    const user = await dependencies.repository.findUser(userId);
    if (!user?.isActive) return { allowed: false, reason: "user not found" };

    const orgRoles = user.roles.filter((role) => role.orgId === orgId);
    return decideRoleResolution({
      userId,
      trainerAdminRoleCodes: user.roles.map((role) => role.code),
      orgAdminRoleCodes: orgRoles.map((role) => role.code),
      roleIds: orgRoles.map((role) => role.id),
      loadOlpEnabled: () => dependencies.repository.getOlpEnabled(orgId),
      action,
    });
  }

  async function decideRoleResolution(input: {
    userId: string;
    trainerAdminRoleCodes: string[];
    orgAdminRoleCodes: string[];
    roleIds: string[];
    loadOlpEnabled: () => Promise<boolean>;
    action: PermissionActionCode;
  }): Promise<RoleResolution> {
    const trainerAdminDecision = dependencies.trainerAdmin.evaluate(input.userId, input.trainerAdminRoleCodes);
    if (trainerAdminDecision?.allowed) return trainerAdminDecision;
    if (input.orgAdminRoleCodes.includes("org_admin")) {
      return { allowed: true, reason: "org_admin" };
    }

    const [olpEnabled, permissionActionId] = await Promise.all([
      input.loadOlpEnabled(),
      getPermissionActionId(input.action),
    ]);
    if (!permissionActionId) return { allowed: false, reason: "unknown action" };
    return {
      roleIds: input.roleIds,
      olpEnabled,
      permissionActionId,
    };
  }

  async function hasRbacCeiling(
    roleIds: string[],
    permissionActionIds: string[],
    resourceType: ResourceType,
  ): Promise<boolean> {
    const cacheKey = `${[...roleIds].sort().join(",")}::${[...permissionActionIds].sort().join(",")}::${resourceType}`;
    const cached = rbacCeilingCache.get(cacheKey);
    if (cached !== undefined) return cached;

    const allowed = await dependencies.repository.hasRbacCeiling({
      roleIds,
      permissionActionIds,
      resourceType,
    });
    rbacCeilingCache.set(cacheKey, allowed);
    return allowed;
  }

  async function queryGrantedIds(input: {
    userId: string;
    orgId: string;
    resourceType: ResourceType;
    resourceIds: string[];
    permissionActionIds: string[];
    roleIds: string[];
  }): Promise<Set<string>> {
    if (input.resourceIds.length === 0) return new Set();
    return new Set(await dependencies.repository.findGrantedResourceIds(input));
  }

  async function resolveActionIds(codes: PermissionActionCode[]): Promise<string[]> {
    const actionIds = await Promise.all(codes.map(getPermissionActionId));
    return actionIds.filter((actionId): actionId is string => actionId !== null);
  }

  async function decideAllowedResources(input: {
    userId: string;
    orgId: string;
    action: PermissionActionCode;
    resourceType: ResourceType;
    resourceIds: string[];
    getAncestorIds?: (resourceId: string) => Promise<string[]> | string[];
    rbacOnly?: boolean;
    preResolved?: RoleResolution;
  }): Promise<AllowedResourcesDecision> {
    const resolution = input.preResolved ?? (await resolveRoles(input.userId, input.orgId, input.action));
    if (!("roleIds" in resolution)) {
      return {
        reason: resolution.reason,
        allowedIds: resolution.allowed ? new Set(input.resourceIds) : new Set(),
      };
    }

    const directActionIds = await resolveActionIds(DIRECT_ACTION_CODES[input.action]);
    if (input.rbacOnly || !resolution.olpEnabled) {
      const allowed = await hasRbacCeiling(resolution.roleIds, directActionIds, input.resourceType);
      return {
        reason: allowed ? null : "no RBAC ceiling",
        allowedIds: allowed ? new Set(input.resourceIds) : new Set(),
      };
    }

    const allowedIds = await queryGrantedIds({
      userId: input.userId,
      orgId: input.orgId,
      resourceType: input.resourceType,
      resourceIds: input.resourceIds,
      permissionActionIds: directActionIds,
      roleIds: resolution.roleIds,
    });
    const inheritedActionIds = await resolveActionIds(INHERITED_ACTION_CODES[input.action]);
    if (inheritedActionIds.length > 0 && input.getAncestorIds) {
      const pendingIds = input.resourceIds.filter((resourceId) => !allowedIds.has(resourceId));
      const ancestorEntries = await Promise.all(
        pendingIds.map(async (resourceId) => [resourceId, await input.getAncestorIds!(resourceId)] as const),
      );
      const ancestorIds = [...new Set(ancestorEntries.flatMap(([, ids]) => ids))];
      const ancestorResourceType = input.resourceType === "metadata_item" ? "folder" : input.resourceType;
      const grantedAncestorIds = await queryGrantedIds({
        userId: input.userId,
        orgId: input.orgId,
        resourceType: ancestorResourceType,
        resourceIds: ancestorIds,
        permissionActionIds: inheritedActionIds,
        roleIds: resolution.roleIds,
      });
      for (const [resourceId, resourceAncestorIds] of ancestorEntries) {
        if (resourceAncestorIds.some((ancestorId) => grantedAncestorIds.has(ancestorId))) {
          allowedIds.add(resourceId);
        }
      }
    }

    return {
      reason: input.resourceIds.every((resourceId) => allowedIds.has(resourceId)) ? null : "no object permission",
      allowedIds,
    };
  }

  function ancestorLoader(request: CanDoRequest): AncestorIdLoader {
    if (!request.orgId) return undefined;
    const snapshot = dependencies.getRequestSnapshot();
    const factMemo =
      snapshot?.userId === request.userId && snapshot.orgId === request.orgId
        ? snapshot.factMemo
        : new Map<string, Promise<unknown>>();

    if (request.resourceType === "folder") {
      return async (resourceId) => {
        const path = await memoize(factMemo, `folder:${request.orgId}:${resourceId}`, () =>
          dependencies.hierarchy.getFolderPath(request.orgId!, request.userId, resourceId),
        );
        return path ? ancestorIdsFromPath(path) : [];
      };
    }
    if (request.resourceType === "metadata_item") {
      return async (resourceId) => {
        const folderId = await memoize(factMemo, `item:${request.orgId}:${resourceId}`, () =>
          dependencies.hierarchy.getMetadataFolderId(request.orgId!, request.userId, resourceId),
        );
        if (!folderId) return [];
        const folderPath = await memoize(factMemo, `folder:${request.orgId}:${folderId}`, () =>
          dependencies.hierarchy.getFolderPath(request.orgId!, request.userId, folderId),
        );
        return [folderId, ...(folderPath ? ancestorIdsFromPath(folderPath) : [])];
      };
    }
    return undefined;
  }

  async function evaluateCanDo(
    request: CanDoRequest,
    getAncestorIds: AncestorIdLoader,
  ): Promise<AuthorizationDecision> {
    if (!request.orgId) return { allowed: false, reason: "no org context" };

    const [resolution, assetEpoch, userEpoch] = await Promise.all([
      resolveRoles(request.userId, request.orgId, request.action),
      dependencies.epochs.getAssetEpoch(request.orgId),
      dependencies.epochs.getUserEpoch(request.orgId, request.userId),
    ]);
    if (!("roleIds" in resolution)) return resolution;

    const roleEpochs = await dependencies.epochs.getRoleEpochs(request.orgId, resolution.roleIds);
    const key = decisionKey({
      orgId: request.orgId,
      userId: request.userId,
      assetEpoch,
      userEpoch,
      roleEpochsHash: hashRoleEpochs(resolution.roleIds, roleEpochs),
      action: request.action,
      resourceType: request.resourceType,
      resourceId: request.resourceId,
    });
    const cached = await dependencies.decisions.read(key);
    if (cached) return cached;

    const result = await dependencies.runSingleFlight(key, async () => {
      const decision = await decideAllowedResources({
        ...request,
        orgId: request.orgId!,
        resourceIds: [request.resourceId],
        getAncestorIds,
        rbacOnly: request.resourceType === "folder" && request.resourceId === request.orgId,
        preResolved: resolution,
      });
      return {
        allowed: decision.allowedIds.has(request.resourceId),
        reason: decision.reason,
      };
    });
    await dependencies.decisions.write(key, result);
    return result;
  }

  async function filterAllowedResourceIds<T extends { id: string }>(
    request: FilterAllowedResourcesRequest<T>,
  ): Promise<Set<string>> {
    if (request.resourceIds.length === 0) return new Set();
    const byId = new Map(request.items?.map((item) => [item.id, item]) ?? []);
    const getAncestorIds =
      request.items && request.getHierarchy
        ? (resourceId: string) => {
            const item = byId.get(resourceId);
            return item ? (request.getHierarchy!(item).ancestorIds ?? []) : [];
          }
        : undefined;
    return (
      await decideAllowedResources({
        ...request,
        getAncestorIds,
      })
    ).allowedIds;
  }

  return {
    canDo: (request) => evaluateCanDo(request, ancestorLoader(request)),
    canDoWithKnownAncestors: (request: KnownAncestorCanDoRequest) => evaluateCanDo(request, () => request.ancestorIds),
    filterAllowedResourceIds,
    async filterVisible<T extends { id: string }>(request: FilterVisibleResourcesRequest<T>): Promise<T[]> {
      const allowedIds = await filterAllowedResourceIds({
        ...request,
        resourceIds: request.items.map((item) => item.id),
      });
      return request.items.filter((item) => allowedIds.has(item.id));
    },
    resetInProcessCaches(): void {
      rbacCeilingCache.clear();
    },
  };
}
