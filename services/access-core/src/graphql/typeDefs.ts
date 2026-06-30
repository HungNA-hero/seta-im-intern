export const typeDefs = /* GraphQL */ `
  directive @auth on FIELD_DEFINITION
  directive @orgMember on FIELD_DEFINITION

  enum ResourceType {
    folder
    metadata_item
  }

  enum PermissionAction {
    read
    write
    delete
    manage_permissions
  }

  type User {
    id: ID!
    email: String!
    displayName: String!
    isActive: Boolean!
    createdAt: String!
    updatedAt: String!
  }

  type Organization {
    id: ID!
    code: String!
    name: String!
    olpEnabled: Boolean!
    createdAt: String!
    updatedAt: String!
  }

  type Role {
    id: ID!
    orgId: ID!
    code: String!
    name: String!
    description: String
    createdAt: String!
    updatedAt: String!
  }

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

  type Mutation {
    createUser(email: String!, displayName: String!): User! @auth
    updateUser(id: ID!, displayName: String!): User! @auth
    deactivateUser(id: ID!): User! @auth
    createOrganization(code: String!, name: String!): Organization! @auth
    createRole(
      orgId: ID!
      code: String!
      name: String!
      description: String
    ): Role! @orgMember
    updateRole(id: ID!, name: String, description: String): Role! @orgMember
    addOrgMember(orgId: ID!, userId: ID!): Boolean! @orgMember
    assignRole(orgId: ID!, userId: ID!, roleId: ID!): Boolean! @orgMember
    revokeRole(orgId: ID!, userId: ID!, roleId: ID!): Boolean! @orgMember
    grantObjectPermission(
      orgId: ID!
      resourceType: ResourceType!
      resourceId: ID!
      action: PermissionAction!
      granteeUserId: ID
      granteeRoleId: ID
      grantedBy: ID!
    ): ObjectPermission! @orgMember
    revokeObjectPermission(id: ID!): Boolean! @orgMember
  }

  type Query {
    users: [User!]! @auth
    user(id: ID!): User @auth
    organizations: [Organization!]! @auth
    organization(id: ID!): Organization @auth
    roles(orgId: ID!): [Role!]! @orgMember
    role(id: ID!): Role @orgMember
    rolePermissions(roleId: ID!): [RolePermission!]! @orgMember
    objectPermissions(
      orgId: ID!
      resourceType: ResourceType!
      resourceId: ID!
    ): [ObjectPermission!]! @orgMember
    canDo(
      userId: ID!
      action: PermissionAction!
      resourceType: ResourceType!
      resourceId: ID!
    ): PermissionResult!
  }
`;
