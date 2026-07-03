import { describe, test, expect, vi, beforeEach } from "vitest";
import { GraphQLError } from "graphql";

const {
  mockCanDo,
  mockGrant,
  mockGetById,
  mockRevoke,
  mockIsActiveOrgMember,
  mockRoleBelongsToOrg,
  mockAssertResourceInOrg,
} = vi.hoisted(() => ({
  mockCanDo: vi.fn(),
  mockGrant: vi.fn(),
  mockGetById: vi.fn(),
  mockRevoke: vi.fn(),
  mockIsActiveOrgMember: vi.fn(),
  mockRoleBelongsToOrg: vi.fn(),
  mockAssertResourceInOrg: vi.fn(),
}));

vi.mock("../db/queries/canDo", () => ({ canDo: mockCanDo }));
vi.mock("../db/queries/objectPermissions", () => ({
  grantObjectPermission: mockGrant,
  listObjectPermissions: vi.fn(),
  revokeObjectPermission: mockRevoke,
  getObjectPermissionById: mockGetById,
}));
vi.mock("../db/queries/rolePermissions", () => ({ listRolePermissions: vi.fn() }));
vi.mock("../db/queries/organizations", () => ({
  isActiveOrgMember: mockIsActiveOrgMember,
  roleBelongsToOrg: mockRoleBelongsToOrg,
}));
vi.mock("../clients/resourceOrg", () => ({
  assertResourceInOrg: mockAssertResourceInOrg,
}));

import { permissionResolvers } from "../graphql/resolvers/permissionResolvers";
import type { GraphQLContext } from "../graphql/context";

function makeCtx(overrides: Partial<GraphQLContext> = {}): GraphQLContext {
  return {
    userId: "user-1",
    currentOrgId: "org-1",
    isMember: true,
    roles: ["org_admin"],
    olpEnabled: false,
    ...overrides,
  };
}

function makeGrantRow(overrides: Record<string, unknown> = {}) {
  return {
    id: "perm-1",
    orgId: "org-1",
    resourceType: "folder",
    resourceId: "folder-1",
    granteeUserId: "grantee-1",
    granteeRoleId: null,
    actionId: "action-read",
    grantedBy: "user-1",
    grantedAt: new Date("2024-01-01T00:00:00Z"),
    ...overrides,
  };
}

beforeEach(() => {
  vi.resetAllMocks();
  mockCanDo.mockResolvedValue({ allowed: true, reason: null });
  mockGrant.mockResolvedValue(makeGrantRow());
  mockGetById.mockResolvedValue(makeGrantRow());
  mockRevoke.mockResolvedValue(undefined);
  mockIsActiveOrgMember.mockResolvedValue(true);
  mockRoleBelongsToOrg.mockResolvedValue(true);
  mockAssertResourceInOrg.mockResolvedValue(undefined);
});

describe("Mutation.grantObjectPermission", () => {
  const base = {
    orgId: "org-1",
    resourceType: "folder" as const,
    resourceId: "folder-1",
    action: "read" as const,
  };

  test("grants to a user when caller has manage_permissions", async () => {
    const result = await permissionResolvers.Mutation.grantObjectPermission(
      undefined,
      { ...base, granteeUserId: "grantee-1" },
      makeCtx(),
    );

    expect(mockCanDo).toHaveBeenCalledWith(
      "user-1",
      "manage_permissions",
      "folder",
      "folder-1",
      "org-1",
    );
    expect(mockGrant).toHaveBeenCalledWith(
      "org-1",
      "folder",
      "folder-1",
      "read",
      "user-1",
      "grantee-1",
      undefined,
    );
    expect(result).toMatchObject({ id: "perm-1", grantedBy: "user-1" });
  });

  test("grants to a role when caller has manage_permissions", async () => {
    await permissionResolvers.Mutation.grantObjectPermission(
      undefined,
      { ...base, granteeRoleId: "role-1" },
      makeCtx(),
    );

    expect(mockGrant).toHaveBeenCalledWith(
      "org-1",
      "folder",
      "folder-1",
      "read",
      "user-1",
      undefined,
      "role-1",
    );
  });

  test("records the authenticated caller as grantor, not a client value", async () => {
    await permissionResolvers.Mutation.grantObjectPermission(
      undefined,
      { ...base, granteeUserId: "grantee-1", grantedBy: "spoofed" } as any,
      makeCtx({ userId: "caller-9" }),
    );

    expect(mockGrant.mock.calls[0][4]).toBe("caller-9");
  });

  test("throws FORBIDDEN and does not grant when canDo denies", async () => {
    mockCanDo.mockResolvedValueOnce({ allowed: false, reason: "no object permission" });

    await expect(
      permissionResolvers.Mutation.grantObjectPermission(
        undefined,
        { ...base, granteeUserId: "grantee-1" },
        makeCtx(),
      ),
    ).rejects.toThrow(
      expect.objectContaining({ extensions: { code: "FORBIDDEN" } }),
    );

    expect(mockGrant).not.toHaveBeenCalled();
  });

  test("throws BAD_USER_INPUT when both grantee fields are set", async () => {
    await expect(
      permissionResolvers.Mutation.grantObjectPermission(
        undefined,
        { ...base, granteeUserId: "grantee-1", granteeRoleId: "role-1" },
        makeCtx(),
      ),
    ).rejects.toThrow(
      expect.objectContaining({ extensions: { code: "BAD_USER_INPUT" } }),
    );

    expect(mockGrant).not.toHaveBeenCalled();
  });

  test("throws BAD_USER_INPUT when neither grantee field is set", async () => {
    await expect(
      permissionResolvers.Mutation.grantObjectPermission(
        undefined,
        { ...base },
        makeCtx(),
      ),
    ).rejects.toThrow(
      expect.objectContaining({ extensions: { code: "BAD_USER_INPUT" } }),
    );

    expect(mockGrant).not.toHaveBeenCalled();
  });

  test("verifies the grantee is a member and the resource is in the org before granting", async () => {
    await permissionResolvers.Mutation.grantObjectPermission(
      undefined,
      { ...base, granteeUserId: "grantee-1" },
      makeCtx(),
    );

    expect(mockIsActiveOrgMember).toHaveBeenCalledWith("org-1", "grantee-1");
    expect(mockAssertResourceInOrg).toHaveBeenCalledWith(
      "folder",
      "folder-1",
      "org-1",
      "user-1",
    );
  });

  test("throws BAD_USER_INPUT and does not grant when grantee is not an active org member", async () => {
    mockIsActiveOrgMember.mockResolvedValueOnce(false);

    await expect(
      permissionResolvers.Mutation.grantObjectPermission(
        undefined,
        { ...base, granteeUserId: "outsider" },
        makeCtx(),
      ),
    ).rejects.toThrow(
      expect.objectContaining({ extensions: { code: "BAD_USER_INPUT" } }),
    );

    expect(mockAssertResourceInOrg).not.toHaveBeenCalled();
    expect(mockGrant).not.toHaveBeenCalled();
  });

  test("throws BAD_USER_INPUT and does not grant when grantee role is from another org", async () => {
    mockRoleBelongsToOrg.mockResolvedValueOnce(false);

    await expect(
      permissionResolvers.Mutation.grantObjectPermission(
        undefined,
        { ...base, granteeRoleId: "role-other-org" },
        makeCtx(),
      ),
    ).rejects.toThrow(
      expect.objectContaining({ extensions: { code: "BAD_USER_INPUT" } }),
    );

    expect(mockAssertResourceInOrg).not.toHaveBeenCalled();
    expect(mockGrant).not.toHaveBeenCalled();
  });

  test("propagates NOT_FOUND and does not grant when the resource is not in the org", async () => {
    mockAssertResourceInOrg.mockRejectedValueOnce(
      new GraphQLError("Resource not found in this organization", {
        extensions: { code: "NOT_FOUND" },
      }),
    );

    await expect(
      permissionResolvers.Mutation.grantObjectPermission(
        undefined,
        { ...base, granteeUserId: "grantee-1" },
        makeCtx(),
      ),
    ).rejects.toThrow(
      expect.objectContaining({ extensions: { code: "NOT_FOUND" } }),
    );

    expect(mockGrant).not.toHaveBeenCalled();
  });

  test("guards run only after canDo authorizes the caller", async () => {
    mockCanDo.mockResolvedValueOnce({ allowed: false, reason: "denied" });

    await expect(
      permissionResolvers.Mutation.grantObjectPermission(
        undefined,
        { ...base, granteeUserId: "grantee-1" },
        makeCtx(),
      ),
    ).rejects.toThrow(
      expect.objectContaining({ extensions: { code: "FORBIDDEN" } }),
    );

    expect(mockIsActiveOrgMember).not.toHaveBeenCalled();
    expect(mockAssertResourceInOrg).not.toHaveBeenCalled();
  });

  test("maps a duplicate grant to CONFLICT", async () => {
    mockGrant.mockRejectedValueOnce({ code: "P2002" });

    await expect(
      permissionResolvers.Mutation.grantObjectPermission(
        undefined,
        { ...base, granteeUserId: "grantee-1" },
        makeCtx(),
      ),
    ).rejects.toThrow(
      expect.objectContaining({ extensions: { code: "CONFLICT" } }),
    );
  });
});

describe("Mutation.revokeObjectPermission", () => {
  test("authorized grantor removes exactly the targeted direct grant", async () => {
    mockGetById.mockResolvedValueOnce(makeGrantRow({ id: "perm-1" }));

    const result = await permissionResolvers.Mutation.revokeObjectPermission(
      undefined,
      { id: "perm-1" },
      makeCtx(),
    );

    expect(mockCanDo).toHaveBeenCalledWith(
      "user-1",
      "manage_permissions",
      "folder",
      "folder-1",
      "org-1",
    );
    expect(mockRevoke).toHaveBeenCalledTimes(1);
    expect(mockRevoke).toHaveBeenCalledWith("perm-1");
    expect(result).toBe(true);
  });

  test("throws FORBIDDEN and does not delete when canDo denies", async () => {
    mockCanDo.mockResolvedValueOnce({ allowed: false, reason: "no object permission" });

    await expect(
      permissionResolvers.Mutation.revokeObjectPermission(
        undefined,
        { id: "perm-1" },
        makeCtx(),
      ),
    ).rejects.toThrow(
      expect.objectContaining({ extensions: { code: "FORBIDDEN" } }),
    );

    expect(mockRevoke).not.toHaveBeenCalled();
  });

  test("throws NOT_FOUND and does not delete when the grant is missing", async () => {
    mockGetById.mockResolvedValueOnce(null);

    await expect(
      permissionResolvers.Mutation.revokeObjectPermission(
        undefined,
        { id: "missing" },
        makeCtx(),
      ),
    ).rejects.toThrow(
      expect.objectContaining({ extensions: { code: "NOT_FOUND" } }),
    );

    expect(mockCanDo).not.toHaveBeenCalled();
    expect(mockRevoke).not.toHaveBeenCalled();
  });

  test("throws FORBIDDEN when the grant belongs to another org", async () => {
    mockGetById.mockResolvedValueOnce(makeGrantRow({ orgId: "org-2" }));

    await expect(
      permissionResolvers.Mutation.revokeObjectPermission(
        undefined,
        { id: "perm-1" },
        makeCtx({ currentOrgId: "org-1" }),
      ),
    ).rejects.toThrow(
      expect.objectContaining({ extensions: { code: "FORBIDDEN" } }),
    );

    expect(mockCanDo).not.toHaveBeenCalled();
    expect(mockRevoke).not.toHaveBeenCalled();
  });

  test("throws UNAUTHENTICATED when the caller is not authenticated", async () => {
    await expect(
      permissionResolvers.Mutation.revokeObjectPermission(
        undefined,
        { id: "perm-1" },
        makeCtx({ userId: null }),
      ),
    ).rejects.toThrow(
      expect.objectContaining({ extensions: { code: "UNAUTHENTICATED" } }),
    );

    expect(mockGetById).not.toHaveBeenCalled();
    expect(mockRevoke).not.toHaveBeenCalled();
  });
});
