import {
  listRolesByOrg,
  getRoleById,
  createRole,
  updateRole,
  Role,
} from "../../db/queries/roles";
import { serializeDates } from './utils';

function toRole(r: Role) {
  return serializeDates({
    id: r.id,
    orgId: r.orgId,
    code: r.code,
    name: r.name,
    description: r.description ?? null,
    createdAt: r.createdAt,
    updatedAt: r.updatedAt,
  });
}

export const roleResolvers = {
  Query: {
    roles: async (_: unknown, { orgId }: { orgId: string }) =>
      (await listRolesByOrg(orgId)).map(toRole),
    role: async (_: unknown, { id }: { id: string }) => {
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
    ) => toRole(await createRole(orgId, code, name, description)),
    updateRole: async (
      _: unknown,
      {
        id,
        name,
        description,
      }: { id: string; name?: string; description?: string },
    ) => toRole(await updateRole(id, name, description)),
  },
};
