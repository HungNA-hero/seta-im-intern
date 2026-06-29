import { userResolvers }         from './userResolvers';
import { roleResolvers }         from './roleResolvers';
import { permissionResolvers }   from './permissionResolvers';
import { organizationResolvers } from './organizationResolvers';
import { folderResolvers }       from './folderResolvers';

export const resolvers = {
  Query: {
    ...userResolvers.Query,
    ...organizationResolvers.Query,
    ...roleResolvers.Query,
    ...permissionResolvers.Query,
    ...folderResolvers.Query,
  },
  Folder: folderResolvers.Folder,
  Mutation: {
    ...roleResolvers.Mutation,
    ...organizationResolvers.Mutation,
  },
};
