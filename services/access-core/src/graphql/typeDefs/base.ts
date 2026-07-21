export const baseTypeDefs = /* GraphQL */ `
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
`;
