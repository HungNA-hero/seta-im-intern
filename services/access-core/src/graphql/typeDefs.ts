export const typeDefs = /* GraphQL */ `
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
    id:          ID!
    email:       String!
    displayName: String!
    isActive:    Boolean!
    createdAt:   String!
    updatedAt:   String!
  }

  type Organization {
    id:         ID!
    code:       String!
    name:       String!
    olpEnabled: Boolean!
    createdAt:  String!
    updatedAt:  String!
  }

  type Role {
    id:          ID!
    orgId:       ID!
    code:        String!
    name:        String!
    description: String
    createdAt:   String!
    updatedAt:   String!
  }

  type RolePermission {
    id:           ID!
    roleId:       ID!
    actionId:     ID!
    resourceType: ResourceType!
  }

  type ObjectPermission {
    id:            ID!
    orgId:         ID!
    resourceType:  ResourceType!
    resourceId:    ID!
    granteeUserId: ID
    granteeRoleId: ID
    actionId:      ID!
    grantedBy:     ID!
    grantedAt:     String!
  }

  type Mutation {
    createRole(orgId: ID!, code: String!, name: String!, description: String): Role!
    updateRole(id: ID!, name: String, description: String): Role!
    addOrgMember(orgId: ID!, userId: ID!): Boolean!
    assignRole(orgId: ID!, userId: ID!, roleId: ID!): Boolean!
    revokeRole(orgId: ID!, userId: ID!, roleId: ID!): Boolean!
  }

  type Query {
    users:                                               [User!]!
    user(id: ID!):                                       User
    organizations:                                       [Organization!]!
    organization(id: ID!):                               Organization
    roles(orgId: ID!):                                   [Role!]!
    role(id: ID!):                                       Role
    rolePermissions(roleId: ID!):                        [RolePermission!]!
    objectPermissions(
      orgId:        ID!
      resourceType: ResourceType!
      resourceId:   ID!
    ):                                                   [ObjectPermission!]!
  }
`;
