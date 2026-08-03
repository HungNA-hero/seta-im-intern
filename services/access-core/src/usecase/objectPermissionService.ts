import { PermissionActionCode, ResourceType } from "@prisma/client";
import { GrantObjectPermissionInput, ObjectPermission } from "../db/queries/objectPermissions";

export interface PermissionActorContext {
  userId: string | null;
  currentOrgId: string | null;
}

export interface GrantPermissionInput {
  orgId: string;
  resourceType: ResourceType;
  resourceId: string;
  action: PermissionActionCode;
  granteeUserId?: string | null;
  granteeRoleId?: string | null;
}

export interface PermissionRepository {
  grant(input: GrantObjectPermissionInput): Promise<ObjectPermission>;
  findById(permissionId: string): Promise<ObjectPermission | null>;
  revoke(permissionId: string): Promise<void>;
}

export interface GranteeDirectory {
  isActiveOrganizationMember(orgId: string, userId: string): Promise<boolean>;
  roleBelongsToOrganization(orgId: string, roleId: string): Promise<boolean>;
}

export interface PermissionAuthorization {
  assertCanManage(input: {
    userId: string;
    orgId: string;
    resourceType: ResourceType;
    resourceId: string;
  }): Promise<void>;
}

export interface PermissionResourceOwnership {
  assertInOrganization(input: {
    resourceType: ResourceType;
    resourceId: string;
    orgId: string;
    userId: string;
  }): Promise<void>;
}

export interface PermissionEpochInvalidator {
  bumpUser(orgId: string, userId: string): Promise<void>;
  bumpRole(orgId: string, roleId: string): Promise<void>;
}

export interface ObjectPermissionServiceDependencies {
  permissions: PermissionRepository;
  grantees: GranteeDirectory;
  authorization: PermissionAuthorization;
  resources: PermissionResourceOwnership;
  epochs: PermissionEpochInvalidator;
  createError(code: string, message: string): Error;
}

export interface ObjectPermissionService {
  grant(actor: PermissionActorContext, input: GrantPermissionInput): Promise<ObjectPermission>;
  revoke(actor: PermissionActorContext, permissionId: string): Promise<void>;
}

function authenticatedUserId(
  actor: PermissionActorContext,
  createError: ObjectPermissionServiceDependencies["createError"],
): string {
  if (!actor.userId) {
    throw createError("UNAUTHENTICATED", "Unauthenticated");
  }
  return actor.userId;
}

function assertSingleGrantee(
  input: GrantPermissionInput,
  createError: ObjectPermissionServiceDependencies["createError"],
): void {
  if (!!input.granteeUserId === !!input.granteeRoleId) {
    throw createError("GRANT_INVALID_TARGET", "Exactly one of granteeUserId or granteeRoleId must be set");
  }
}

function rethrowGrantPersistenceError(
  error: unknown,
  createError: ObjectPermissionServiceDependencies["createError"],
): never {
  const code = (error as { code?: unknown })?.code;
  if (code === "P2002") {
    throw createError("BAD_USER_INPUT", "Object permission already exists");
  }
  if (code === "P2025") {
    throw createError("UNKNOWN_ACTION", "Permission action not found");
  }
  throw error;
}

export function createObjectPermissionService(
  dependencies: ObjectPermissionServiceDependencies,
): ObjectPermissionService {
  async function assertValidGrantee(input: GrantPermissionInput): Promise<void> {
    const valid = input.granteeUserId
      ? await dependencies.grantees.isActiveOrganizationMember(input.orgId, input.granteeUserId)
      : await dependencies.grantees.roleBelongsToOrganization(input.orgId, input.granteeRoleId!);
    if (!valid) {
      throw dependencies.createError(
        "BAD_USER_INPUT",
        input.granteeUserId
          ? "Grantee is not an active member of this organization"
          : "Grantee role does not belong to this organization",
      );
    }
  }

  async function bumpEpoch(
    permission: Pick<ObjectPermission, "orgId" | "granteeUserId" | "granteeRoleId">,
  ): Promise<void> {
    if (permission.granteeUserId) {
      await dependencies.epochs.bumpUser(permission.orgId, permission.granteeUserId);
    } else if (permission.granteeRoleId) {
      await dependencies.epochs.bumpRole(permission.orgId, permission.granteeRoleId);
    }
  }

  return {
    async grant(actor, input): Promise<ObjectPermission> {
      const userId = authenticatedUserId(actor, dependencies.createError);
      assertSingleGrantee(input, dependencies.createError);
      await dependencies.authorization.assertCanManage({
        userId,
        orgId: input.orgId,
        resourceType: input.resourceType,
        resourceId: input.resourceId,
      });
      await Promise.all([
        assertValidGrantee(input),
        dependencies.resources.assertInOrganization({
          resourceType: input.resourceType,
          resourceId: input.resourceId,
          orgId: input.orgId,
          userId,
        }),
      ]);

      let permission: ObjectPermission;
      try {
        permission = await dependencies.permissions.grant({
          ...input,
          grantedBy: userId,
        });
      } catch (error) {
        rethrowGrantPersistenceError(error, dependencies.createError);
      }
      await bumpEpoch(permission);
      return permission;
    },

    async revoke(actor, permissionId): Promise<void> {
      const userId = authenticatedUserId(actor, dependencies.createError);
      const permission = await dependencies.permissions.findById(permissionId);
      if (!permission) {
        throw dependencies.createError("GRANT_NOT_FOUND", "Object permission not found");
      }
      if (actor.currentOrgId !== permission.orgId) {
        throw dependencies.createError("FORBIDDEN", "Forbidden");
      }

      await dependencies.authorization.assertCanManage({
        userId,
        orgId: permission.orgId,
        resourceType: permission.resourceType as ResourceType,
        resourceId: permission.resourceId,
      });
      await dependencies.permissions.revoke(permissionId);
      await bumpEpoch(permission);
    },
  };
}
