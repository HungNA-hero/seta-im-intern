export const permissionTypeDefs = /* GraphQL */ `
  type RolePermission {
    id: ID!
    roleId: ID!
    actionId: ID!
    resourceType: ResourceType!
  }

  type ObjectPermission {
    id: ID!
    orgId: ID!
    resourceType: ResourceType!
    resourceId: ID!
    granteeUserId: ID
    granteeRoleId: ID
    actionId: ID!
    grantedBy: ID!
    grantedAt: String!
  }

  type PermissionResult {
    allowed: Boolean!
    reason: String
  }
`;

export const permissionQueryFields = /* GraphQL */ `
  rolePermissions(roleId: ID!): [RolePermission!]! @orgAdmin
  objectPermissions(
    orgId: ID!
    resourceType: ResourceType!
    resourceId: ID!
  ): [ObjectPermission!]! @orgMember
  canDo(
    action: PermissionAction!
    resourceType: ResourceType!
    resourceId: ID!
  ): PermissionResult! @orgMember
`;

export const permissionMutationFields = /* GraphQL */ `
  grantObjectPermission(
    orgId: ID!
    resourceType: ResourceType!
    resourceId: ID!
    action: PermissionAction!
    granteeUserId: ID
    granteeRoleId: ID
  ): ObjectPermission! @orgMember @sameOrg
  revokeObjectPermission(id: ID!): Boolean! @orgMember
`;
