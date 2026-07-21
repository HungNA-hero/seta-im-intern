export const metadataTypeDefs = /* GraphQL */ `
  type MetadataItem {
    id: ID!
    folderId: ID!
    title: String!
    description: String
    labels: [String!]!
    category: String
    externalSource: String
    externalId: String
    sourceUrl: String
    thumbnailUrl: String
    license: String
    author: String
    metadataJson: String!
    notes: String
    createdBy: ID!
    updatedBy: ID
    createdAt: String!
    updatedAt: String!
  }

  input CreateMetadataInput {
    folderId: ID!
    title: String!
    description: String
    labels: [String!]
    category: String
    externalSource: String
    externalId: String
    sourceUrl: String
    thumbnailUrl: String
    license: String
    author: String
    metadataJson: String
    notes: String
  }

  input UpdateMetadataInput {
    title: String
    description: String
    labels: [String!]
    category: String
    externalSource: String
    externalId: String
    sourceUrl: String
    thumbnailUrl: String
    license: String
    author: String
    metadataJson: String
    notes: String
  }

  input MetadataSearchInput {
    folderId: ID
    query: String
    labels: [String!]
    category: String
    externalSource: String
    limit: Int = 50
    offset: Int = 0
  }

  input MetadataConnectionSearchInput {
    folderId: ID!
    query: String
    labels: [String!]
    category: String
    externalSource: String
    first: Int = 50
    after: String
  }

  type MetadataPageInfo {
    endCursor: String
    hasNextPage: Boolean!
  }

  type MetadataItemConnection {
    nodes: [MetadataItem!]!
    pageInfo: MetadataPageInfo!
  }
`;

export const metadataQueryFields = /* GraphQL */ `
  metadataItems(orgId: ID!, folderId: ID!): [MetadataItem!]!
    @orgMember
    @sameOrg
  metadataItem(orgId: ID!, id: ID!): MetadataItem @orgMember @sameOrg
  searchMetadata(orgId: ID!, input: MetadataSearchInput!): [MetadataItem!]!
    @orgMember
    @sameOrg
    @deprecated(reason: "Use searchMetadataConnection")
  searchMetadataConnection(
    orgId: ID!
    input: MetadataConnectionSearchInput!
  ): MetadataItemConnection! @orgMember @sameOrg
`;

export const metadataMutationFields = /* GraphQL */ `
  createMetadata(orgId: ID!, input: CreateMetadataInput!): MetadataItem!
    @orgMember
    @sameOrg
  updateMetadata(
    orgId: ID!
    id: ID!
    input: UpdateMetadataInput!
  ): MetadataItem! @orgMember @sameOrg
  deleteMetadata(orgId: ID!, id: ID!): Boolean! @orgMember @sameOrg
`;
