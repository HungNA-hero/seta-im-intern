import { prisma } from "../prisma";

export type Role = {
  id: string;
  orgId: string;
  code: string;
  name: string;
  description: string | null;
  createdAt: Date;
  updatedAt: Date;
};

function toRole(r: any): Role {
  return {
    id: r.id,
    orgId: r.org_id,
    code: r.code,
    name: r.name,
    description: r.description,
    createdAt: r.created_at,
    updatedAt: r.updated_at,
  };
}

export async function listRolesByOrg(orgId: string): Promise<Role[]> {
  const roles = await prisma.roles.findMany({ where: { org_id: orgId } });
  return roles.map(toRole);
}

export async function getRoleById(id: string): Promise<Role | null> {
  const r = await prisma.roles.findUnique({ where: { id } });
  return r ? toRole(r) : null;
}

export async function createRole(
  orgId: string,
  code: string,
  name: string,
  description?: string,
): Promise<Role> {
  const r = await prisma.roles.create({ data: { org_id: orgId, code, name, description } });
  return toRole(r);
}

export async function updateRole(
  id: string,
  name?: string,
  description?: string,
): Promise<Role> {
  const r = await prisma.roles.update({
    where: { id },
    data: { name, description },
  });
  return toRole(r);
}
