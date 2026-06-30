import { listOrganizations, getOrganizationById, addOrgMember, createOrganization, Organization } from "../../db/queries/organizations";
import { assignRole, revokeRole } from "../../db/queries/userRoles";
import { serializeDates, rethrowPrismaError } from "./utils";
import { GraphQLContext } from "../context";
import { GraphQLError } from "graphql";

function toOrganization(o: Organization) {
  return serializeDates(o);
}

export const organizationResolvers = {
  Query: {
    organizations: async () => (await listOrganizations()).map(toOrganization),
    organization: async (_: unknown, { id }: { id: string }) => {
      const o = await getOrganizationById(id);
      if (!o) throw new GraphQLError("Organization not found", { extensions: { code: "NOT_FOUND" } });
      return toOrganization(o);
    },
  },
  Mutation: {
    createOrganization: async (_: unknown, { code, name }: { code: string; name: string }, ctx: GraphQLContext) => {
      try {
        const org = await createOrganization(code, name);
        await addOrgMember(org.id, ctx.userId as string);
        return toOrganization(org);
      } catch (err) {
        rethrowPrismaError(err, { P2002: "Organization code already in use" });
      }
    },
    addOrgMember: async (_: unknown, { orgId, userId }: { orgId: string; userId: string }) => {
      try {
        await addOrgMember(orgId, userId);
        return true;
      } catch (err) {
        rethrowPrismaError(err, { P2002: "User is already a member of this organization" });
      }
    },
    assignRole: async (_: unknown, { orgId, userId, roleId }: { orgId: string; userId: string; roleId: string }) => {
      try {
        await assignRole(orgId, userId, roleId);
        return true;
      } catch (err) {
        rethrowPrismaError(err, { P2002: "Role already assigned to this user" });
      }
    },
    revokeRole: async (_: unknown, { orgId, userId, roleId }: { orgId: string; userId: string; roleId: string }) => {
      try {
        await revokeRole(orgId, userId, roleId);
        return true;
      } catch (err) {
        rethrowPrismaError(err, { P2025: "Role assignment not found" });
      }
    },
  },
};
