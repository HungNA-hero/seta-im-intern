import { beforeEach, describe, expect, test, vi } from "vitest";

const { mockGetRoleById, mockAssignRole, mockRevokeRole } = vi.hoisted(() => ({
  mockGetRoleById: vi.fn(),
  mockAssignRole: vi.fn(),
  mockRevokeRole: vi.fn(),
}));

vi.mock("../db/queries/roles", () => ({ getRoleById: mockGetRoleById }));
vi.mock("../db/queries/userRoles", () => ({
  assignRole: mockAssignRole,
  revokeRole: mockRevokeRole,
}));
vi.mock("../db/queries/organizations", () => ({
  listOrganizations: vi.fn(),
  getOrganizationById: vi.fn(),
  addOrgMember: vi.fn(),
  createOrganization: vi.fn(),
}));

import { organizationResolvers } from "../graphql/resolvers/organizationResolvers";

beforeEach(() => vi.resetAllMocks());

describe("reserved role assignment protection", () => {
  test.each(["org_admin", "trainer_admin", " ORG_ADMIN "])(
    "rejects assigning reserved role code %s",
    async (code) => {
      mockGetRoleById.mockResolvedValue({ id: "role-1", orgId: "org-1", code });
      await expect(
        organizationResolvers.Mutation.assignRole({}, {
          orgId: "org-1", userId: "user-2", roleId: "role-1",
        }),
      ).rejects.toMatchObject({ extensions: { code: "RESERVED_ROLE_CODE" } });
      expect(mockAssignRole).not.toHaveBeenCalled();
    },
  );

  test("rejects assigning a role from a different organization", async () => {
    mockGetRoleById.mockResolvedValue({ id: "role-1", orgId: "org-2", code: "viewer" });
    await expect(
      organizationResolvers.Mutation.assignRole({}, {
        orgId: "org-1", userId: "user-2", roleId: "role-1",
      }),
    ).rejects.toMatchObject({ extensions: { code: "NOT_FOUND" } });
    expect(mockAssignRole).not.toHaveBeenCalled();
  });

  test("rejects revoking a reserved role", async () => {
    mockGetRoleById.mockResolvedValue({ id: "role-1", orgId: "org-1", code: "org_admin" });
    await expect(
      organizationResolvers.Mutation.revokeRole({}, {
        orgId: "org-1", userId: "user-2", roleId: "role-1",
      }),
    ).rejects.toMatchObject({ extensions: { code: "RESERVED_ROLE_CODE" } });
    expect(mockRevokeRole).not.toHaveBeenCalled();
  });
});
