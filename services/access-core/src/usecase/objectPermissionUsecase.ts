import { bumpRoleEpoch, bumpUserEpoch } from "../cache/epoch";
import { GraphQLError } from "graphql";
import { assertResourceInOrg } from "../clients/resourceOrg";
import {
  getObjectPermissionById,
  grantObjectPermission,
  revokeObjectPermission,
} from "../db/queries/objectPermissions";
import { isActiveOrgMember, roleBelongsToOrg } from "../db/queries/organizations";
import { assertCan, GraphQLContext } from "../graphql/context";
import { createObjectPermissionService, GrantPermissionInput } from "./objectPermissionService";

export type { GrantPermissionInput } from "./objectPermissionService";

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
      assertCan({
        userId,
        action: "manage_permissions",
        resourceType,
        resourceId,
        orgId,
      }),
  },
  resources: {
    assertInOrganization: ({ resourceType, resourceId, orgId, userId }) =>
      assertResourceInOrg(resourceType, resourceId, orgId, userId),
  },
  epochs: {
    bumpUser: bumpUserEpoch,
    bumpRole: bumpRoleEpoch,
  },
  createError: (code, message) => new GraphQLError(message, { extensions: { code } }),
});

export function grantPermission(ctx: GraphQLContext, input: GrantPermissionInput) {
  return objectPermissionService.grant(ctx, input);
}

export function revokePermission(ctx: GraphQLContext, permissionId: string) {
  return objectPermissionService.revoke(ctx, permissionId);
}
