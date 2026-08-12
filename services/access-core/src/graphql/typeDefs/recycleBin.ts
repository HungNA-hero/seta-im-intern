export const recycleBinTypeDefs = /* GraphQL */ `
  enum RecycleBinResourceType {
    FOLDER
    METADATA
  }

  type RecycleBinEntry {
    lifecycleUnitId: ID!
    resourceType: RecycleBinResourceType!
    resourceId: ID!
    displayName: String!
    deletedAt: String!
  }

  input RecycleBinConnectionInput {
    first: Int = 50
    after: String
  }

  type RecycleBinPageInfo {
    endCursor: String
    hasNextPage: Boolean!
  }

  type RecycleBinEntryConnection {
    nodes: [RecycleBinEntry!]!
    pageInfo: RecycleBinPageInfo!
  }
`;

export const recycleBinQueryFields = /* GraphQL */ `
  recycleBin(orgId: ID!, input: RecycleBinConnectionInput!): RecycleBinEntryConnection!
    @orgMember
    @sameOrg
`;
