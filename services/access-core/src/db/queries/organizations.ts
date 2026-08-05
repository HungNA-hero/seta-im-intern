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
  return orgs.map((org) => ({
    id: org.id,
    code: org.code,
    name: org.name,
    olpEnabled: org.olpEnabled,
    createdAt: org.createdAt,
    updatedAt: org.updatedAt,
  }));
}

export async function getOrganizationById(id: string): Promise<Organization | null> {
  const org = await prisma.organization.findUnique({ where: { id } });
  if (!org) return null;
  return {
    id: org.id,
    code: org.code,
    name: org.name,
    olpEnabled: org.olpEnabled,
    createdAt: org.createdAt,
    updatedAt: org.updatedAt,
  };
}

export async function addOrgMember(orgId: string, userId: string): Promise<void> {
  await prisma.organizationMember.create({ data: { orgId, userId } });
}

/** Returns whether the user is active and belongs to the requested organization. */
export async function isActiveOrgMember(orgId: string, userId: string): Promise<boolean> {
  const activeMember = await prisma.user.findFirst({
    where: { id: userId, isActive: true, orgMembers: { some: { orgId } } },
    select: { id: true },
  });
  return activeMember !== null;
}

/** Returns whether the role is owned by the requested organization. */
export async function roleBelongsToOrg(orgId: string, roleId: string): Promise<boolean> {
  const role = await prisma.role.findFirst({
    where: { id: roleId, orgId },
    select: { id: true },
  });
  return role !== null;
}

export async function createOrganization(code: string, name: string): Promise<Organization> {
  const org = await prisma.organization.create({ data: { code, name } });
  return {
    id: org.id,
    code: org.code,
    name: org.name,
    olpEnabled: org.olpEnabled,
    createdAt: org.createdAt,
    updatedAt: org.updatedAt,
  };
}
