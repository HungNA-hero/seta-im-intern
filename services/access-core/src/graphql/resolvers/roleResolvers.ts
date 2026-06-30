import { listRolesByOrg, getRoleById, createRole, updateRole } from "../../db/queries/roles";
import { serializeDates, rethrowPrismaError } from "./utils";
import { GraphQLContext } from "../context";
import { GraphQLError } from "graphql";

export const roleResolvers = {
  Query: {
    roles: async (_: unknown, { orgId }: { orgId: string }) =>
      (await listRolesByOrg(orgId)).map(serializeDates),
    role: async (_: unknown, { id }: { id: string }, ctx: GraphQLContext) => {
      const r = await getRoleById(id);
      if (!r || r.orgId !== ctx.currentOrgId)
        throw new GraphQLError("Role not found", { extensions: { code: "NOT_FOUND" } });
      return serializeDates(r);
    },
  },
  Mutation: {
    createRole: async (
      _: unknown,
      { orgId, code, name, description }: { orgId: string; code: string; name: string; description?: string },
    ) => {
      try {
        return serializeDates(await createRole(orgId, code, name, description));
      } catch (err) {
        rethrowPrismaError(err, { P2002: "Role code already exists in this org" });
      }
    },
    updateRole: async (
      _: unknown,
      { id, name, description }: { id: string; name?: string; description?: string },
    ) => {
      try {
        return serializeDates(await updateRole(id, name, description));
      } catch (err) {
        rethrowPrismaError(err, { P2025: "Role not found" });
      }
    },
  },
};
