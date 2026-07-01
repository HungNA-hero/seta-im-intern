import { userResolvers } from "./userResolvers";
import { roleResolvers } from "./roleResolvers";
import { permissionResolvers } from "./permissionResolvers";
import { organizationResolvers } from "./organizationResolvers";
import { canDoResolvers } from "./canDoResolvers";
import { folderResolvers } from "./folderResolvers";

export const resolvers = {
  Query: {
    ...userResolvers.Query,
    ...organizationResolvers.Query,
    ...roleResolvers.Query,
    ...permissionResolvers.Query,
    ...canDoResolvers.Query,
    ...folderResolvers.Query,
  },
  Mutation: {
    ...userResolvers.Mutation,
    ...organizationResolvers.Mutation,
    ...roleResolvers.Mutation,
    ...permissionResolvers.Mutation,
    ...folderResolvers.Mutation,
  },
  Folder: folderResolvers.Folder,
};
