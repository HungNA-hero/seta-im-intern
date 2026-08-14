import { baseTypeDefs } from "./base";
import { folderMutationFields, folderQueryFields, folderTypeDefs } from "./folder";
import { metadataMutationFields, metadataQueryFields, metadataTypeDefs } from "./metadata";
import { recycleBinQueryFields, recycleBinTypeDefs } from "./recycleBin";
import { lifecycleMutationFields, lifecycleQueryFields, lifecycleTypeDefs } from "./lifecycle";
import {
  organizationCreateMutationFields,
  organizationMemberMutationFields,
  organizationQueryFields,
  organizationTypeDefs,
} from "./organization";
import { permissionMutationFields, permissionQueryFields, permissionTypeDefs } from "./permission";
import { roleAssignmentMutationFields, roleDefinitionMutationFields, roleQueryFields, roleTypeDefs } from "./role";
import { userMutationFields, userQueryFields, userTypeDefs } from "./user";

const mutationFields = [
  userMutationFields,
  organizationCreateMutationFields,
  roleDefinitionMutationFields,
  organizationMemberMutationFields,
  roleAssignmentMutationFields,
  permissionMutationFields,
  folderMutationFields,
  metadataMutationFields,
  lifecycleMutationFields,
].join("\n");

const queryFields = [
  userQueryFields,
  organizationQueryFields,
  roleQueryFields,
  permissionQueryFields,
  folderQueryFields,
  metadataQueryFields,
  recycleBinQueryFields,
  lifecycleQueryFields,
].join("\n");

export const typeDefs = /* GraphQL */ `
  ${baseTypeDefs}
  ${userTypeDefs}
  ${organizationTypeDefs}
  ${roleTypeDefs}
  ${permissionTypeDefs}
  type Mutation {
    ${mutationFields}
  }
  ${folderTypeDefs}
  ${metadataTypeDefs}
  ${recycleBinTypeDefs}
  ${lifecycleTypeDefs}
  type Query {
    ${queryFields}
  }
`;
