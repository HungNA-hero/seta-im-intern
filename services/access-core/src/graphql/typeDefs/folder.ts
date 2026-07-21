export const folderTypeDefs = /* GraphQL */ `
  type Folder {
    id: ID!
    orgId: ID!
    path: String!
    name: String!
    description: String
    createdBy: ID!
    updatedBy: ID
    createdAt: String!
    updatedAt: String!
    children: [Folder!]!
  }
`;

export const folderQueryFields = /* GraphQL */ `
  folder(orgId: ID!, id: ID!): Folder @auth @sameOrg
  folderTree(orgId: ID!, rootPath: String): [Folder!]! @auth @sameOrg
  folderChildren(orgId: ID!, parentPath: String!): [Folder!]! @auth @sameOrg
`;

export const folderMutationFields = /* GraphQL */ `
  createFolder(
    orgId: ID!
    parentPath: String
    name: String!
    description: String
  ): Folder! @orgMember @sameOrg
  updateFolder(
    orgId: ID!
    id: ID!
    name: String
    description: String
  ): Folder! @orgMember @sameOrg
  moveFolder(orgId: ID!, id: ID!, destinationParentId: ID): Folder!
    @orgMember
    @sameOrg
  deleteFolder(orgId: ID!, id: ID!): Boolean! @orgMember @sameOrg
`;
