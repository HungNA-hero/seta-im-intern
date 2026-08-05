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

function toRole(role: {
  id: string;
  orgId: string;
  code: string;
  name: string;
  description: string | null;
  createdAt: Date;
  updatedAt: Date;
}): Role {
  return {
    id: role.id,
    orgId: role.orgId,
    code: role.code,
    name: role.name,
    description: role.description,
    createdAt: role.createdAt,
    updatedAt: role.updatedAt,
  };
}

export async function listRolesByOrg(orgId: string): Promise<Role[]> {
  const roles = await prisma.role.findMany({ where: { orgId } });
  return roles.map(toRole);
}

export async function getRoleById(id: string): Promise<Role | null> {
  const role = await prisma.role.findUnique({ where: { id } });
  return role ? toRole(role) : null;
}

export async function createRole(orgId: string, code: string, name: string, description?: string): Promise<Role> {
  const role = await prisma.role.create({ data: { orgId, code, name, description } });
  return toRole(role);
}

export async function updateRole(id: string, name?: string, description?: string): Promise<Role> {
  const role = await prisma.role.update({
    where: { id },
    data: { name, description },
  });
  return toRole(role);
}
