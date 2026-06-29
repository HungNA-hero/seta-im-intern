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
  const orgs = await prisma.organization.findMany({ orderBy: { createdAt: "asc" } });
  return orgs.map((o) => ({
    id: o.id,
    code: o.code,
    name: o.name,
    olpEnabled: o.olpEnabled,
    createdAt: o.createdAt,
    updatedAt: o.updatedAt,
  }));
}

export async function getOrganizationById(id: string): Promise<Organization | null> {
  const o = await prisma.organization.findUnique({ where: { id } });
  if (!o) return null;
  return {
    id: o.id,
    code: o.code,
    name: o.name,
    olpEnabled: o.olpEnabled,
    createdAt: o.createdAt,
    updatedAt: o.updatedAt,
  };
}

export async function addOrgMember(
  orgId: string,
  userId: string,
): Promise<void> {
  await prisma.organizationMember.create({ data: { orgId, userId } });
}
