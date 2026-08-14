import { userResolvers } from "./userResolvers";
import { roleResolvers } from "./roleResolvers";
import { permissionResolvers } from "./permissionResolvers";
import { organizationResolvers } from "./organizationResolvers";
import { canDoResolvers } from "./canDoResolvers";
import { folderResolvers } from "./folderResolvers";
import { metadataResolvers } from "./metadataResolvers";
import { recycleBinResolvers } from "./recycleBinResolvers";
import { lifecycleResolvers } from "./lifecycleResolvers";

export const resolvers = {
  Query: {
    ...userResolvers.Query,
    ...organizationResolvers.Query,
    ...roleResolvers.Query,
    ...permissionResolvers.Query,
    ...canDoResolvers.Query,
    ...folderResolvers.Query,
    ...metadataResolvers.Query,
    ...recycleBinResolvers.Query,
    ...lifecycleResolvers.Query,
  },
  Mutation: {
    ...userResolvers.Mutation,
    ...organizationResolvers.Mutation,
    ...roleResolvers.Mutation,
    ...permissionResolvers.Mutation,
    ...folderResolvers.Mutation,
    ...metadataResolvers.Mutation,
    ...lifecycleResolvers.Mutation,
  },
  Folder: folderResolvers.Folder,
};
