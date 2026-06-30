import { defaultFieldResolver, GraphQLSchema } from "graphql";
import { mapSchema, getDirective, MapperKind } from "@graphql-tools/utils";
import { assertAuthenticated, assertOrgMember, GraphQLContext } from "./context";

function wrapField(
  schema: GraphQLSchema,
  directiveName: string,
  guard: (ctx: GraphQLContext) => void,
): GraphQLSchema {
  return mapSchema(schema, {
    [MapperKind.OBJECT_FIELD](fieldConfig) {
      if (!getDirective(schema, fieldConfig, directiveName)?.[0]) {
        return fieldConfig;
      }
      const { resolve = defaultFieldResolver } = fieldConfig;
      fieldConfig.resolve = (source, args, ctx: GraphQLContext, info) => {
        guard(ctx);
        return resolve(source, args, ctx, info);
      };
      return fieldConfig;
    },
  });
}

export function applyAuthDirectives(schema: GraphQLSchema): GraphQLSchema {
  schema = wrapField(schema, "auth", assertAuthenticated);
  schema = wrapField(schema, "orgMember", assertOrgMember);
  return schema;
}
