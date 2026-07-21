export const organizationTypeDefs = /* GraphQL */ `
  type Organization {
    id: ID!
    code: String!
    name: String!
    olpEnabled: Boolean!
    createdAt: String!
    updatedAt: String!
  }
`;

export const organizationQueryFields = /* GraphQL */ `
  organizations: [Organization!]! @auth
  organization(id: ID!): Organization @auth
`;

export const organizationCreateMutationFields = /* GraphQL */ `
  createOrganization(code: String!, name: String!): Organization! @trainerAdmin
`;

export const organizationMemberMutationFields = /* GraphQL */ `
  addOrgMember(orgId: ID!, userId: ID!): Boolean! @orgAdmin @sameOrg
`;
