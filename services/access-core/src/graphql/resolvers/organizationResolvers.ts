import { listOrganizations, getOrganizationById, addOrgMember, Organization } from '../../db/queries/organizations';
import { assignRole, revokeRole } from '../../db/queries/userRoles';
import { serializeDates } from './utils';
import { assertAuthenticated, assertOrgMember, GraphQLContext } from '../context';

function toOrganization(o: Organization) {
  return serializeDates({
    id: o.id,
    code: o.code,
    name: o.name,
    olpEnabled: o.olpEnabled,
    createdAt: o.createdAt,
    updatedAt: o.updatedAt,
  });
}

export const organizationResolvers = {
  Query: {
    organizations: async (_: unknown, __: unknown, ctx: GraphQLContext) => {
      assertAuthenticated(ctx);
      return (await listOrganizations()).map(toOrganization);
    },
    organization: async (_: unknown, { id }: { id: string }, ctx: GraphQLContext) => {
      assertAuthenticated(ctx);
      const o = await getOrganizationById(id);
      return o ? toOrganization(o) : null;
    },
  },
  Mutation: {
    addOrgMember: async (_: unknown, { orgId, userId }: { orgId: string; userId: string }, ctx: GraphQLContext) => {
      assertOrgMember(ctx);
      await addOrgMember(orgId, userId);
      return true;
    },
    assignRole: async (_: unknown, { orgId, userId, roleId }: { orgId: string; userId: string; roleId: string }, ctx: GraphQLContext) => {
      assertOrgMember(ctx);
      await assignRole(orgId, userId, roleId);
      return true;
    },
    revokeRole: async (_: unknown, { orgId, userId, roleId }: { orgId: string; userId: string; roleId: string }, ctx: GraphQLContext) => {
      assertOrgMember(ctx);
      await revokeRole(orgId, userId, roleId);
      return true;
    },
  },
};
