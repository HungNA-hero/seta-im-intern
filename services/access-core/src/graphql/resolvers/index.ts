import { userResolvers }         from './userResolvers';
import { roleResolvers }         from './roleResolvers';
import { permissionResolvers }   from './permissionResolvers';
import { organizationResolvers } from './organizationResolvers';

export const resolvers = {
  Query: {
    ...userResolvers.Query,
    ...organizationResolvers.Query,
    ...roleResolvers.Query,
    ...permissionResolvers.Query,
  },
  Mutation: {
    ...roleResolvers.Mutation,
    ...organizationResolvers.Mutation,
  },
};
