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

export async function addOrgMember(orgId: string, userId: string): Promise<void> {
  await prisma.organizationMember.create({ data: { orgId, userId } });
}

/** Returns whether the user is active and belongs to the requested organization. */
export async function isActiveOrgMember(orgId: string, userId: string): Promise<boolean> {
  const u = await prisma.user.findFirst({
    where: { id: userId, isActive: true, orgMembers: { some: { orgId } } },
    select: { id: true },
  });
  return u !== null;
}

/** Returns whether the role is owned by the requested organization. */
export async function roleBelongsToOrg(orgId: string, roleId: string): Promise<boolean> {
  const r = await prisma.role.findFirst({
    where: { id: roleId, orgId },
    select: { id: true },
  });
  return r !== null;
}

export async function createOrganization(code: string, name: string): Promise<Organization> {
  const o = await prisma.organization.create({ data: { code, name } });
  return { id: o.id, code: o.code, name: o.name, olpEnabled: o.olpEnabled, createdAt: o.createdAt, updatedAt: o.updatedAt };
}
