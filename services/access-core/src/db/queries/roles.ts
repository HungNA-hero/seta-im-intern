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

export async function listRolesByOrg(orgId: string): Promise<Role[]> {
  const roles = await prisma.roles.findMany({ where: { org_id: orgId } });
  return roles.map((r) => ({
    id: r.id,
    orgId: r.org_id,
    code: r.code,
    name: r.name,
    description: r.description,
    createdAt: r.created_at,
    updatedAt: r.updated_at,
  }));
}

export async function getRoleById(id: string): Promise<Role | null> {
  const r = await prisma.roles.findUnique({ where: { id } });
  if (!r) return null;
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
