import { beforeEach, describe, expect, test, vi } from "vitest";
import { getErrorDefinition } from "../errors/errorCodes";

const { mockPrisma } = vi.hoisted(() => ({
  mockPrisma: {
    permissionAction: { findUnique: vi.fn() },
    objectPermission: { create: vi.fn() },
  },
}));

vi.mock("../db/prisma", () => ({ prisma: mockPrisma }));

import { grantObjectPermission } from "../db/queries/objectPermissions";

const grant = {
  orgId: "org-1",
  resourceType: "folder",
  resourceId: "folder-1",
  action: "read",
  grantedBy: "user-1",
  granteeUserId: "grantee-1",
} as const;

describe("grantObjectPermission", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockPrisma.permissionAction.findUnique.mockResolvedValue({ id: "action-read" });
  });

  test("reports an unrecognized action by name rather than as a persistence failure", async () => {
    mockPrisma.permissionAction.findUnique.mockResolvedValue(null);

    await expect(grantObjectPermission({ ...grant })).rejects.toMatchObject({
      message: getErrorDefinition("UNKNOWN_ACTION").message,
      extensions: expect.objectContaining({ code: "UNKNOWN_ACTION" }),
    });
    expect(mockPrisma.objectPermission.create).not.toHaveBeenCalled();
  });

  test("translates a unique-constraint violation into a domain error at the seam", async () => {
    mockPrisma.objectPermission.create.mockRejectedValue({ code: "P2002" });

    await expect(grantObjectPermission({ ...grant })).rejects.toMatchObject({
      extensions: expect.objectContaining({ code: "BAD_USER_INPUT" }),
    });
  });

  test("lets an unrecognized persistence failure through untranslated", async () => {
    const failure = Object.assign(new Error("connection lost"), { code: "P1001" });
    mockPrisma.objectPermission.create.mockRejectedValue(failure);

    await expect(grantObjectPermission({ ...grant })).rejects.toBe(failure);
  });

  test("resolves the action code to an id before writing the grant", async () => {
    mockPrisma.objectPermission.create.mockResolvedValue({ id: "perm-1" });

    await grantObjectPermission({ ...grant });

    expect(mockPrisma.permissionAction.findUnique).toHaveBeenCalledWith({ where: { code: "read" } });
    expect(mockPrisma.objectPermission.create).toHaveBeenCalledWith({
      data: expect.objectContaining({ actionId: "action-read", granteeRoleId: null }),
    });
  });
});
