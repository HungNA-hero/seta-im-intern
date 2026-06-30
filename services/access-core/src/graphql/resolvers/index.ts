import { userResolvers } from "./userResolvers";
import { roleResolvers } from "./roleResolvers";
import { permissionResolvers } from "./permissionResolvers";
import { organizationResolvers } from "./organizationResolvers";
import { canDoResolvers } from "./canDoResolvers";

export const resolvers = {
  Query: {
    ...userResolvers.Query,
    ...organizationResolvers.Query,
    ...roleResolvers.Query,
    ...permissionResolvers.Query,
    ...canDoResolvers.Query,
  },
  Mutation: {
    ...userResolvers.Mutation,
    ...organizationResolvers.Mutation,
    ...roleResolvers.Mutation,
    ...permissionResolvers.Mutation,
  },
};
