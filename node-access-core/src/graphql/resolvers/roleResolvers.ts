import { listRolesByOrg, getRoleById, Role } from '../../db/queries/roles';

function toRole(r: Role) {
  return {
    id:          r.id,
    orgId:       r.orgId,
    code:        r.code,
    name:        r.name,
    description: r.description ?? null,
    createdAt:   r.createdAt.toISOString(),
    updatedAt:   r.updatedAt.toISOString(),
  };
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
};
