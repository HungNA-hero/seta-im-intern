import { defaultFieldResolver, GraphQLSchema } from "graphql";
import { mapSchema, getDirective, MapperKind } from "@graphql-tools/utils";
import {
  assertAuthenticated,
  assertOrgAdmin,
  assertOrgContext,
  assertOrgMember,
  assertTrainerAdmin,
  GraphQLContext,
} from "./context";

type FieldGuard = (
  ctx: GraphQLContext,
  args: Record<string, unknown>,
) => void | Promise<void>;

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
      fieldConfig.resolve = async (source, args, ctx: GraphQLContext, info) => {
        await guard(ctx, args);
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
  schema = wrapField(schema, "sameOrg", assertSameOrg);
  schema = wrapField(schema, "auth", assertAuthenticated);
  schema = wrapField(schema, "orgMember", assertOrgMember);
  schema = wrapField(schema, "orgAdmin", assertOrgAdmin);
  schema = wrapField(schema, "trainerAdmin", (ctx) => assertTrainerAdmin(ctx));
  return schema;
}
