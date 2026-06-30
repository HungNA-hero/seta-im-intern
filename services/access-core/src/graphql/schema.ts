import { createSchema } from "graphql-yoga";
import { typeDefs } from "./typeDefs";
import { resolvers } from "./resolvers";
import { applyAuthDirectives } from "./directives";

export const schema = applyAuthDirectives(
  createSchema({ typeDefs, resolvers }),
);
