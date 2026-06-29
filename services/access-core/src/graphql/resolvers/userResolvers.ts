import { listUsers, getUserById, User } from "../../db/queries/users";
import { serializeDates } from "./utils";
import { assertAuthenticated, GraphQLContext } from "../context";

function toUser(u: User) {
  return serializeDates(u);
}

export const userResolvers = {
  Query: {
    users: async (_: unknown, __: unknown, ctx: GraphQLContext) => {
      assertAuthenticated(ctx);
      return (await listUsers()).map(toUser);
    },
    user: async (_: unknown, { id }: { id: string }, ctx: GraphQLContext) => {
      assertAuthenticated(ctx);
      const u = await getUserById(id);
      return u ? toUser(u) : null;
    },
  },
};
