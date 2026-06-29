import {
  listRolesByOrg,
  getRoleById,
  createRole,
  updateRole,
  Role,
} from "../../db/queries/roles";
import { serializeDates } from './utils';
import { assertAuthenticated, assertOrgMember, GraphQLContext } from '../context';

function toRole(r: Role) {
  return serializeDates(r);
}

export const roleResolvers = {
  Query: {
    roles: async (_: unknown, { orgId }: { orgId: string }, ctx: GraphQLContext) => {
      assertOrgMember(ctx);
      return (await listRolesByOrg(orgId)).map(toRole);
    },
    role: async (_: unknown, { id }: { id: string }, ctx: GraphQLContext) => {
      assertAuthenticated(ctx);
      const r = await getRoleById(id);
      return r ? toRole(r) : null;
    },
  },
  Mutation: {
    createRole: async (
      _: unknown,
      {
        orgId,
        code,
        name,
        description,
      }: { orgId: string; code: string; name: string; description?: string },
      ctx: GraphQLContext,
    ) => {
      assertOrgMember(ctx);
      return toRole(await createRole(orgId, code, name, description));
    },
    updateRole: async (
      _: unknown,
      {
        id,
        name,
        description,
      }: { id: string; name?: string; description?: string },
      ctx: GraphQLContext,
    ) => {
      assertOrgMember(ctx);
      return toRole(await updateRole(id, name, description));
    },
  },
};
