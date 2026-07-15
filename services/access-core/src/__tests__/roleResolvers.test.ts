import { describe, test, expect, vi, beforeEach } from "vitest";

const { mockGetRoleById, mockCreateRole, mockUpdateRole } = vi.hoisted(() => ({
  mockGetRoleById: vi.fn(),
  mockCreateRole: vi.fn(),
  mockUpdateRole: vi.fn(),
}));

vi.mock("../db/queries/roles", () => ({
  listRolesByOrg: vi.fn(),
  getRoleById: mockGetRoleById,
  createRole: mockCreateRole,
  updateRole: mockUpdateRole,
}));

import { roleResolvers } from "../graphql/resolvers/roleResolvers";
import type { GraphQLContext } from "../graphql/context";

function ctx(overrides: Partial<GraphQLContext> = {}): GraphQLContext {
  return {
    userId: "user-1",
    currentOrgId: "org-1",
    isMember: true,
    roles: ["org_admin"],
    olpEnabled: false,
    ...overrides,
  };
}

beforeEach(() => {
  vi.resetAllMocks();
});

describe("createRole", () => {
  test.each(["trainer_admin", "org_admin"])(
    "rejects reserved role code %s",
    async (code) => {
      await expect(
        roleResolvers.Mutation.createRole(
          {},
          { orgId: "org-1", code, name: "x" },
        ),
      ).rejects.toMatchObject({ extensions: { code: "RESERVED_ROLE_CODE" } });
      expect(mockCreateRole).not.toHaveBeenCalled();
    },
  );

  test("allows a non-reserved role code", async () => {
    mockCreateRole.mockResolvedValueOnce({
      id: "role-1",
      orgId: "org-1",
      code: "viewer2",
      name: "Viewer2",
      description: null,
      createdAt: new Date(),
      updatedAt: new Date(),
    });

    await roleResolvers.Mutation.createRole(
      {},
      { orgId: "org-1", code: "viewer2", name: "Viewer2" },
    );

    expect(mockCreateRole).toHaveBeenCalledWith("org-1", "viewer2", "Viewer2", undefined);
  });
});

describe("updateRole", () => {
  test("rejects updating a role belonging to a different org", async () => {
    mockGetRoleById.mockResolvedValueOnce({
      id: "role-1",
      orgId: "org-2",
      code: "viewer",
      name: "Viewer",
      description: null,
      createdAt: new Date(),
      updatedAt: new Date(),
    });

    await expect(
      roleResolvers.Mutation.updateRole(
        {},
        { id: "role-1", name: "New name" },
        ctx({ currentOrgId: "org-1" }),
      ),
    ).rejects.toMatchObject({ extensions: { code: "NOT_FOUND" } });
    expect(mockUpdateRole).not.toHaveBeenCalled();
  });

  test("rejects updating a nonexistent role", async () => {
    mockGetRoleById.mockResolvedValueOnce(null);

    await expect(
      roleResolvers.Mutation.updateRole(
        {},
        { id: "role-1", name: "New name" },
        ctx(),
      ),
    ).rejects.toMatchObject({ extensions: { code: "NOT_FOUND" } });
    expect(mockUpdateRole).not.toHaveBeenCalled();
  });

  test("allows updating a role belonging to the caller's own org", async () => {
    mockGetRoleById.mockResolvedValueOnce({
      id: "role-1",
      orgId: "org-1",
      code: "viewer",
      name: "Viewer",
      description: null,
      createdAt: new Date(),
      updatedAt: new Date(),
    });
    mockUpdateRole.mockResolvedValueOnce({
      id: "role-1",
      orgId: "org-1",
      code: "viewer",
      name: "New name",
      description: null,
      createdAt: new Date(),
      updatedAt: new Date(),
    });

    await roleResolvers.Mutation.updateRole(
      {},
      { id: "role-1", name: "New name" },
      ctx({ currentOrgId: "org-1" }),
    );

    expect(mockUpdateRole).toHaveBeenCalledWith("role-1", "New name", undefined);
  });
});
