import { describe, expect, test, vi } from "vitest";
import { createObjectPermissionService } from "../usecase/objectPermissionService";
import { getErrorDefinition } from "../errors/errorCodes";

function fixture() {
  const permission = {
    id: "permission-1",
    orgId: "org-1",
    resourceType: "folder",
    resourceId: "folder-1",
    granteeUserId: "grantee-1",
    granteeRoleId: null,
    actionId: "action-read",
    grantedBy: "actor-1",
    grantedAt: new Date("2026-01-01T00:00:00Z"),
  };
  const dependencies = {
    permissions: {
      grant: vi.fn().mockResolvedValue(permission),
      findById: vi.fn().mockResolvedValue(permission),
      revoke: vi.fn().mockResolvedValue(undefined),
    },
    grantees: {
      isActiveOrganizationMember: vi.fn().mockResolvedValue(true),
      roleBelongsToOrganization: vi.fn().mockResolvedValue(true),
    },
    authorization: {
      assertCanManage: vi.fn().mockResolvedValue(undefined),
    },
    resources: {
      assertInOrganization: vi.fn().mockResolvedValue(undefined),
    },
    epochs: {
      bumpUser: vi.fn().mockResolvedValue(undefined),
      bumpRole: vi.fn().mockResolvedValue(undefined),
    },
  };
  return { permission, dependencies };
}

describe("createObjectPermissionService", () => {
  test("coordinates grant dependencies and invalidates the user epoch", async () => {
    const { permission, dependencies } = fixture();
    const service = createObjectPermissionService(dependencies);

    await expect(
      service.grant(
        { userId: "actor-1", currentOrgId: "org-1" },
        {
          orgId: "org-1",
          resourceType: "folder",
          resourceId: "folder-1",
          action: "read",
          granteeUserId: "grantee-1",
        },
      ),
    ).resolves.toEqual(permission);

    expect(dependencies.permissions.grant).toHaveBeenCalledWith(expect.objectContaining({ grantedBy: "actor-1" }));
    expect(dependencies.epochs.bumpUser).toHaveBeenCalledWith("org-1", "grantee-1");
  });

  test("rejects an ambiguous grantee before invoking dependencies", async () => {
    const { dependencies } = fixture();
    const service = createObjectPermissionService(dependencies);

    await expect(
      service.grant(
        { userId: "actor-1", currentOrgId: "org-1" },
        {
          orgId: "org-1",
          resourceType: "folder",
          resourceId: "folder-1",
          action: "read",
          granteeUserId: "user-1",
          granteeRoleId: "role-1",
        },
      ),
    ).rejects.toMatchObject({
      extensions: { code: "GRANT_INVALID_TARGET" },
    });
    expect(dependencies.authorization.assertCanManage).not.toHaveBeenCalled();
  });

  test("reports a missing grant with the message the shared error table defines", async () => {
    const { dependencies } = fixture();
    dependencies.permissions.findById = vi.fn().mockResolvedValue(null);
    const service = createObjectPermissionService(dependencies);

    await expect(service.revoke({ userId: "actor-1", currentOrgId: "org-1" }, "missing")).rejects.toMatchObject({
      message: getErrorDefinition("GRANT_NOT_FOUND").message,
      extensions: expect.objectContaining({ code: "GRANT_NOT_FOUND", number: 5001 }),
    });
    expect(dependencies.permissions.revoke).not.toHaveBeenCalled();
  });

  test("reports an ambiguous grantee with the message the shared error table defines", async () => {
    const { dependencies } = fixture();
    const service = createObjectPermissionService(dependencies);

    await expect(
      service.grant(
        { userId: "actor-1", currentOrgId: "org-1" },
        {
          orgId: "org-1",
          resourceType: "folder",
          resourceId: "folder-1",
          action: "read",
          granteeUserId: "grantee-1",
          granteeRoleId: "role-1",
        },
      ),
    ).rejects.toMatchObject({
      message: getErrorDefinition("GRANT_INVALID_TARGET").message,
      extensions: expect.objectContaining({ code: "GRANT_INVALID_TARGET" }),
    });
    expect(dependencies.permissions.grant).not.toHaveBeenCalled();
  });

  test("refuses to revoke a grant belonging to another organization", async () => {
    const { dependencies } = fixture();
    const service = createObjectPermissionService(dependencies);

    await expect(
      service.revoke({ userId: "actor-1", currentOrgId: "org-other" }, "permission-1"),
    ).rejects.toMatchObject({ extensions: expect.objectContaining({ code: "FORBIDDEN" }) });
    expect(dependencies.authorization.assertCanManage).not.toHaveBeenCalled();
    expect(dependencies.permissions.revoke).not.toHaveBeenCalled();
  });
});
