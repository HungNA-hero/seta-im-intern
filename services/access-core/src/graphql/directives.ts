import { defaultFieldResolver, GraphQLSchema } from "graphql";
import { mapSchema, getDirective, MapperKind } from "@graphql-tools/utils";
import {
  assertAuthenticated,
  assertOrgContext,
  assertOrgMember,
  GraphQLContext,
} from "./context";

type FieldGuard = (ctx: GraphQLContext, args: Record<string, unknown>) => void;

function wrapField(
  schema: GraphQLSchema,
  directiveName: string,
  guard: FieldGuard,
): GraphQLSchema {
  return mapSchema(schema, {
    [MapperKind.OBJECT_FIELD](fieldConfig) {
      if (!getDirective(schema, fieldConfig, directiveName)?.[0]) {
        return fieldConfig;
      }
      const { resolve = defaultFieldResolver } = fieldConfig;
      fieldConfig.resolve = (source, args, ctx: GraphQLContext, info) => {
        guard(ctx, args);
        return resolve(source, args, ctx, info);
      };
      return fieldConfig;
    },
  });
}

function assertSameOrg(
  ctx: GraphQLContext,
  args: Record<string, unknown>,
): void {
  assertOrgContext(ctx, args.orgId as string);
}

export function applyAuthDirectives(schema: GraphQLSchema): GraphQLSchema {
  // Wrappers execute in reverse application order. Apply same-org first so
  // authentication and membership always fail before argument-org checks.
  schema = wrapField(schema, "sameOrg", assertSameOrg);
  schema = wrapField(schema, "auth", assertAuthenticated);
  schema = wrapField(schema, "orgMember", assertOrgMember);
  return schema;
}
