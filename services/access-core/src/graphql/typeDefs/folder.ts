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

  enum FolderDeletionStatus {
    queued
    running
    succeeded
    failed
    cancelled
  }

  type FolderDeletionPreview {
    id: ID!
    rootFolderId: ID!
    activeFolderCount: Int!
    activeMetadataCount: Int!
    tombstoneFolderCount: Int!
    tombstoneMetadataCount: Int!
    totalRows: Int!
    confirmationToken: String!
    expiresAt: String!
  }

  type FolderDeletionJob {
    id: ID!
    orgId: ID!
    rootFolderId: ID!
    requestedBy: ID!
    status: FolderDeletionStatus!
    activeFolderCount: Int!
    activeMetadataCount: Int!
    tombstoneFolderCount: Int!
    tombstoneMetadataCount: Int!
    deletedFolderCount: Int!
    deletedMetadataCount: Int!
    attempts: Int!
    manualRetries: Int!
    lastErrorCode: String
    queuedAt: String
    startedAt: String
    completedAt: String
    cancelledAt: String
  }
`;

export const folderQueryFields = /* GraphQL */ `
  folder(orgId: ID!, id: ID!): Folder @auth @sameOrg
  folderTree(orgId: ID!, rootPath: String): [Folder!]! @auth @sameOrg
  folderChildren(orgId: ID!, parentPath: String!): [Folder!]! @auth @sameOrg
  folderDeletionJob(orgId: ID!, id: ID!): FolderDeletionJob! @orgMember @sameOrg
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
  previewFolderDeletion(orgId: ID!, folderId: ID!): FolderDeletionPreview!
    @orgMember
    @sameOrg
  confirmFolderDeletion(
    orgId: ID!
    folderId: ID!
    previewId: ID!
    confirmationToken: String!
  ): FolderDeletionJob! @orgMember @sameOrg
  cancelFolderDeletion(orgId: ID!, id: ID!): FolderDeletionJob! @orgMember @sameOrg
  retryFolderDeletion(orgId: ID!, id: ID!): FolderDeletionJob! @orgMember @sameOrg
`;
