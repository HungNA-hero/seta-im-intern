export const typeDefs = /* GraphQL */ `
  directive @auth on FIELD_DEFINITION
  directive @orgMember on FIELD_DEFINITION
  directive @sameOrg on FIELD_DEFINITION
  directive @orgAdmin on FIELD_DEFINITION
  directive @trainerAdmin on FIELD_DEFINITION

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
    createUser(email: String!, displayName: String!): User! @trainerAdmin
    updateUser(id: ID!, displayName: String!): User! @trainerAdmin
    deactivateUser(id: ID!): User! @trainerAdmin
    createOrganization(code: String!, name: String!): Organization! @trainerAdmin
    createRole(
      orgId: ID!
      code: String!
      name: String!
      description: String
    ): Role! @orgAdmin @sameOrg
    updateRole(id: ID!, name: String, description: String): Role! @orgAdmin
    addOrgMember(orgId: ID!, userId: ID!): Boolean! @orgAdmin @sameOrg
    assignRole(orgId: ID!, userId: ID!, roleId: ID!): Boolean! @orgAdmin @sameOrg
    revokeRole(orgId: ID!, userId: ID!, roleId: ID!): Boolean! @orgAdmin @sameOrg
    grantObjectPermission(
      orgId: ID!
      resourceType: ResourceType!
      resourceId: ID!
      action: PermissionAction!
      granteeUserId: ID
      granteeRoleId: ID
    ): ObjectPermission! @orgMember @sameOrg
    revokeObjectPermission(id: ID!): Boolean! @orgMember
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
    createMetadata(orgId: ID!, input: CreateMetadataInput!): MetadataItem!
      @orgMember
      @sameOrg
    updateMetadata(
      orgId: ID!
      id: ID!
      input: UpdateMetadataInput!
    ): MetadataItem! @orgMember @sameOrg
    deleteMetadata(orgId: ID!, id: ID!): Boolean! @orgMember @sameOrg
  }

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

  type Query {
    users: [User!]! @auth
    user(id: ID!): User @auth
    organizations: [Organization!]! @auth
    organization(id: ID!): Organization @auth
    roles(orgId: ID!): [Role!]! @orgAdmin @sameOrg
    role(id: ID!): Role @orgAdmin
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
    folder(orgId: ID!, id: ID!): Folder @auth @sameOrg
    folderTree(orgId: ID!, rootPath: String): [Folder!]! @auth @sameOrg
    folderChildren(orgId: ID!, parentPath: String!): [Folder!]! @auth @sameOrg
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
  }
`;
