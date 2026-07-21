export const roleTypeDefs = /* GraphQL */ `
  type Role {
    id: ID!
    orgId: ID!
    code: String!
    name: String!
    description: String
    createdAt: String!
    updatedAt: String!
  }
`;

export const roleQueryFields = /* GraphQL */ `
  roles(orgId: ID!): [Role!]! @orgAdmin @sameOrg
  role(id: ID!): Role @orgAdmin
`;

export const roleDefinitionMutationFields = /* GraphQL */ `
  createRole(
    orgId: ID!
    code: String!
    name: String!
    description: String
  ): Role! @orgAdmin @sameOrg
  updateRole(id: ID!, name: String, description: String): Role! @orgAdmin
`;

export const roleAssignmentMutationFields = /* GraphQL */ `
  assignRole(orgId: ID!, userId: ID!, roleId: ID!): Boolean! @orgAdmin @sameOrg
  revokeRole(orgId: ID!, userId: ID!, roleId: ID!): Boolean! @orgAdmin @sameOrg
`;
