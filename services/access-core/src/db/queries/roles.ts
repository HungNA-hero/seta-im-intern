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

function toRole(r: {
  id: string;
  orgId: string;
  code: string;
  name: string;
  description: string | null;
  createdAt: Date;
  updatedAt: Date;
}): Role {
  return {
    id: r.id,
    orgId: r.orgId,
    code: r.code,
    name: r.name,
    description: r.description,
    createdAt: r.createdAt,
    updatedAt: r.updatedAt,
  };
}

export async function listRolesByOrg(orgId: string): Promise<Role[]> {
  const roles = await prisma.role.findMany({ where: { orgId } });
  return roles.map(toRole);
}

export async function getRoleById(id: string): Promise<Role | null> {
  const r = await prisma.role.findUnique({ where: { id } });
  return r ? toRole(r) : null;
}

export async function createRole(
  orgId: string,
  code: string,
  name: string,
  description?: string,
): Promise<Role> {
  const r = await prisma.role.create({ data: { orgId, code, name, description } });
  return toRole(r);
}

export async function updateRole(
  id: string,
  name?: string,
  description?: string,
): Promise<Role> {
  const r = await prisma.role.update({
    where: { id },
    data: { name, description },
  });
  return toRole(r);
}
