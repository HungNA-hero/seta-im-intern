import { userResolvers }       from './userResolvers';
import { roleResolvers }       from './roleResolvers';
import { permissionResolvers } from './permissionResolvers';

export const resolvers = {
  Query: {
    ...userResolvers.Query,
    ...roleResolvers.Query,
    ...permissionResolvers.Query,
  },
};
