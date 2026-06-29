import { prisma } from "../prisma";

export type Organization = {
  id: string;
  code: string;
  name: string;
  olpEnabled: boolean;
  createdAt: Date;
  updatedAt: Date;
};

export async function listOrganizations(): Promise<Organization[]> {
  const orgs = await prisma.organizations.findMany({ orderBy: { created_at: "asc" } });
  return orgs.map((o) => ({
    id: o.id,
    code: o.code,
    name: o.name,
    olpEnabled: o.olp_enabled,
    createdAt: o.created_at,
    updatedAt: o.updated_at,
  }));
}

export async function getOrganizationById(id: string): Promise<Organization | null> {
  const o = await prisma.organizations.findUnique({ where: { id } });
  if (!o) return null;
  return {
    id: o.id,
    code: o.code,
    name: o.name,
    olpEnabled: o.olp_enabled,
    createdAt: o.created_at,
    updatedAt: o.updated_at,
  };
}

export async function addOrgMember(
  orgId: string,
  userId: string,
): Promise<void> {
  await prisma.organization_members.create({ data: { org_id: orgId, user_id: userId } });
}
