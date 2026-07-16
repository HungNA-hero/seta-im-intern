import { listRolesByOrg, getRoleById, createRole, updateRole } from "../../db/queries/roles";
import { serializeDates, rethrowPrismaError } from "./utils";
import { GraphQLContext } from "../context";
import { GraphQLError } from "graphql";

const RESERVED_ROLE_CODES = new Set(["trainer_admin", "org_admin"]);

function isReservedRoleCode(code: string): boolean {
  return RESERVED_ROLE_CODES.has(code.trim().toLowerCase());
}

export const roleResolvers = {
  Query: {
    roles: async (_: unknown, { orgId }: { orgId: string }) =>
      (await listRolesByOrg(orgId)).map(serializeDates),
    role: async (_: unknown, { id }: { id: string }, ctx: GraphQLContext) => {
      const r = await getRoleById(id);
      if (!r || r.orgId !== ctx.currentOrgId)
        throw new GraphQLError("Role not found", { extensions: { code: "BAD_USER_INPUT" } });
      return serializeDates(r);
    },
  },
  Mutation: {
    createRole: async (
      _: unknown,
      { orgId, code, name, description }: { orgId: string; code: string; name: string; description?: string },
    ) => {
      if (isReservedRoleCode(code)) {
        throw new GraphQLError("Role code is reserved and cannot be created", {
          extensions: { code: "RESERVED_ROLE_CODE" },
        });
      }
      try {
        return serializeDates(await createRole(orgId, code, name, description));
      } catch (err) {
        rethrowPrismaError(err, {
          P2002: { message: "Role code already exists in this org", errorCode: "BAD_USER_INPUT" },
        });
      }
    },
    updateRole: async (
      _: unknown,
      { id, name, description }: { id: string; name?: string; description?: string },
      ctx: GraphQLContext,
    ) => {
      const existing = await getRoleById(id);
      if (!existing || existing.orgId !== ctx.currentOrgId)
        throw new GraphQLError("Role not found", { extensions: { code: "BAD_USER_INPUT" } });
      if (isReservedRoleCode(existing.code)) {
        throw new GraphQLError("Reserved role cannot be modified", {
          extensions: { code: "RESERVED_ROLE_CODE" },
        });
      }
      try {
        return serializeDates(await updateRole(id, name, description));
      } catch (err) {
        rethrowPrismaError(err, {
          P2025: { message: "Role not found", errorCode: "BAD_USER_INPUT" },
        });
      }
    },
  },
};
