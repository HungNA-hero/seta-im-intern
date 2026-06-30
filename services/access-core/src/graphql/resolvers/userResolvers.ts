import { listUsers, getUserById, createUser, updateUser, deactivateUser } from "../../db/queries/users";
import { serializeDates, rethrowPrismaError } from "./utils";
import { GraphQLError } from "graphql";

export const userResolvers = {
  Query: {
    users: async () => (await listUsers()).map(serializeDates),
    user: async (_: unknown, { id }: { id: string }) => {
      const u = await getUserById(id);
      if (!u) throw new GraphQLError("User not found", { extensions: { code: "NOT_FOUND" } });
      return serializeDates(u);
    },
  },
  Mutation: {
    createUser: async (_: unknown, { email, displayName }: { email: string; displayName: string }) => {
      try {
        return serializeDates(await createUser(email, displayName));
      } catch (err) {
        rethrowPrismaError(err, { P2002: "Email already in use" });
      }
    },
    updateUser: async (_: unknown, { id, displayName }: { id: string; displayName: string }) => {
      try {
        return serializeDates(await updateUser(id, displayName));
      } catch (err) {
        rethrowPrismaError(err, { P2025: "User not found" });
      }
    },
    deactivateUser: async (_: unknown, { id }: { id: string }) => {
      try {
        return serializeDates(await deactivateUser(id));
      } catch (err) {
        rethrowPrismaError(err, { P2025: "User not found" });
      }
    },
  },
};
