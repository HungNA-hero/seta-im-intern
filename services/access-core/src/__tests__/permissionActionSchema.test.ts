import { buildASTSchema, parse } from "graphql";
import { describe, expect, test } from "vitest";
import { baseTypeDefs } from "../graphql/typeDefs/base";

describe("public PermissionAction schema", () => {
  test("exposes only read, write, and manage_permissions", () => {
    const schema = buildASTSchema(parse(baseTypeDefs));
    const permissionAction = schema.getType("PermissionAction");

    expect(permissionAction?.toString()).toBe("PermissionAction");
    expect((permissionAction as any).getValues().map((value: { name: string }) => value.name)).toEqual([
      "read",
      "write",
      "manage_permissions",
    ]);
  });
});
