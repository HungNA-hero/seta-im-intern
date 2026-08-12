import { PermissionActionCode, ResourceType } from "@prisma/client";
import { bumpRoleEpoch, bumpUserEpoch } from "../cache/epoch";
import { assertResourceInOrg } from "../clients/resourceOrg";
import {
  getObjectPermissionById,
  grantObjectPermission,
  GrantObjectPermissionInput,
  ObjectPermission,
  revokeObjectPermission,
} from "../db/queries/objectPermissions";
import { isActiveOrgMember, roleBelongsToOrg } from "../db/queries/organizations";
import { badUserInput, forbidden, grantInvalidTarget, grantNotFound, unauthenticated } from "../errors/factories";
import { assertCan, GraphQLContext } from "../graphql/context";

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
}

export interface ObjectPermissionService {
  grant(actor: PermissionActorContext, input: GrantPermissionInput): Promise<ObjectPermission>;
  revoke(actor: PermissionActorContext, permissionId: string): Promise<void>;
}

function authenticatedUserId(actor: PermissionActorContext): string {
  if (!actor.userId) {
    throw unauthenticated();
  }
  return actor.userId;
}

function assertSingleGrantee(input: GrantPermissionInput): void {
  if (!!input.granteeUserId === !!input.granteeRoleId) {
    throw grantInvalidTarget();
  }
}

export function createObjectPermissionService(
  dependencies: ObjectPermissionServiceDependencies,
): ObjectPermissionService {
  async function assertValidGrantee(input: GrantPermissionInput): Promise<void> {
    const valid = input.granteeUserId
      ? await dependencies.grantees.isActiveOrganizationMember(input.orgId, input.granteeUserId)
      : await dependencies.grantees.roleBelongsToOrganization(input.orgId, input.granteeRoleId!);
    if (!valid) {
      throw badUserInput(
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
      const userId = authenticatedUserId(actor);
      assertSingleGrantee(input);
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

      const permission = await dependencies.permissions.grant({ ...input, grantedBy: userId });
      await bumpEpoch(permission);
      return permission;
    },

    async revoke(actor, permissionId): Promise<void> {
      const userId = authenticatedUserId(actor);
      const permission = await dependencies.permissions.findById(permissionId);
      if (!permission) {
        throw grantNotFound();
      }
      if (actor.currentOrgId !== permission.orgId) {
        throw forbidden("Forbidden");
      }

      await dependencies.authorization.assertCanManage({
        userId,
        orgId: permission.orgId,
        resourceType: permission.resourceType,
        resourceId: permission.resourceId,
      });
      await dependencies.permissions.revoke(permissionId);
      await bumpEpoch(permission);
    },
  };
}

const objectPermissionService = createObjectPermissionService({
  permissions: {
    grant: grantObjectPermission,
    findById: getObjectPermissionById,
    revoke: revokeObjectPermission,
  },
  grantees: {
    isActiveOrganizationMember: isActiveOrgMember,
    roleBelongsToOrganization: roleBelongsToOrg,
  },
  authorization: {
    assertCanManage: ({ userId, orgId, resourceType, resourceId }) =>
      assertCan({ userId, action: "manage_permissions", resourceType, resourceId, orgId }),
  },
  resources: {
    assertInOrganization: ({ resourceType, resourceId, orgId, userId }) =>
      assertResourceInOrg(resourceType, resourceId, orgId, userId),
  },
  epochs: {
    bumpUser: bumpUserEpoch,
    bumpRole: bumpRoleEpoch,
  },
});

export function grantPermission(ctx: GraphQLContext, input: GrantPermissionInput) {
  return objectPermissionService.grant(ctx, input);
}

export function revokePermission(ctx: GraphQLContext, permissionId: string) {
  return objectPermissionService.revoke(ctx, permissionId);
}
