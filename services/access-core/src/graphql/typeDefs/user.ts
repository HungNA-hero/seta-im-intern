export const userTypeDefs = /* GraphQL */ `
  type User {
    id: ID!
    email: String!
    displayName: String!
    isActive: Boolean!
    createdAt: String!
    updatedAt: String!
  }
`;

export const userQueryFields = /* GraphQL */ `
  users: [User!]! @auth
  user(id: ID!): User @auth
`;

export const userMutationFields = /* GraphQL */ `
  createUser(email: String!, displayName: String!): User! @trainerAdmin
  updateUser(id: ID!, displayName: String!): User! @trainerAdmin
  deactivateUser(id: ID!): User! @trainerAdmin
`;
