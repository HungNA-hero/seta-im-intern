import { prisma } from "../prisma";
import type { Organization } from "@prisma/client";

export type { Organization };

export function listOrganizations(): Promise<Organization[]> {
  return prisma.organization.findMany({ orderBy: { createdAt: "asc" } });
}

export function getOrganizationById(id: string): Promise<Organization | null> {
  return prisma.organization.findUnique({ where: { id } });
}

export async function addOrgMember(
  orgId: string,
  userId: string,
): Promise<void> {
  await prisma.organizationMember.create({ data: { orgId, userId } });
}
