import { listOrganizations, getOrganizationById, addOrgMember, createOrganization, Organization } from "../../db/queries/organizations";
import { assignRole, revokeRole } from "../../db/queries/userRoles";
import { serializeDates, rethrowPrismaError } from "./utils";
import { GraphQLContext } from "../context";
import { GraphQLError } from "graphql";
import { getRoleById } from "../../db/queries/roles";
import { bumpUserEpoch } from "../../cache/epoch";

const RESERVED_ROLE_CODES = new Set(["trainer_admin", "org_admin"]);

function assertAssignableRole(
  role: { orgId: string; code: string } | null,
  orgId: string,
): void {
  if (!role || role.orgId !== orgId) {
    throw new GraphQLError("Role not found", { extensions: { code: "BAD_USER_INPUT" } });
  }
  if (RESERVED_ROLE_CODES.has(role.code.trim().toLowerCase())) {
    throw new GraphQLError("Reserved role cannot be assigned or revoked", {
      extensions: { code: "RESERVED_ROLE_CODE" },
    });
  }
}

function toOrganization(o: Organization) {
  return serializeDates(o);
}

export const organizationResolvers = {
  Query: {
    organizations: async () => (await listOrganizations()).map(toOrganization),
    organization: async (_: unknown, { id }: { id: string }) => {
      const o = await getOrganizationById(id);
      if (!o) throw new GraphQLError("Organization not found", { extensions: { code: "BAD_USER_INPUT" } });
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
        rethrowPrismaError(err, {
          P2002: { message: "Organization code already in use", errorCode: "BAD_USER_INPUT" },
        });
      }
    },
    addOrgMember: async (_: unknown, { orgId, userId }: { orgId: string; userId: string }) => {
      try {
        await addOrgMember(orgId, userId);
        return true;
      } catch (err) {
        rethrowPrismaError(err, {
          P2002: { message: "User is already a member of this organization", errorCode: "BAD_USER_INPUT" },
        });
      }
    },
    assignRole: async (_: unknown, { orgId, userId, roleId }: { orgId: string; userId: string; roleId: string }) => {
      try {
        assertAssignableRole(await getRoleById(roleId), orgId);
        await assignRole(orgId, userId, roleId);
      } catch (err) {
        rethrowPrismaError(err, {
          P2002: { message: "Role already assigned to this user", errorCode: "BAD_USER_INPUT" },
        });
      }
      await bumpUserEpoch(orgId, userId);
      return true;
    },
    revokeRole: async (_: unknown, { orgId, userId, roleId }: { orgId: string; userId: string; roleId: string }) => {
      try {
        assertAssignableRole(await getRoleById(roleId), orgId);
        await revokeRole(orgId, userId, roleId);
      } catch (err) {
        rethrowPrismaError(err, {
          P2025: { message: "Role assignment not found", errorCode: "BAD_USER_INPUT" },
        });
      }
      await bumpUserEpoch(orgId, userId);
      return true;
    },
  },
};
