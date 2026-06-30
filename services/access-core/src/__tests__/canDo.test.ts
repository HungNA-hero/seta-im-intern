import { describe, test, expect, vi, beforeEach } from "vitest";

const { mockPrisma } = vi.hoisted(() => ({
  mockPrisma: {
    user: { findUnique: vi.fn() },
    organization: { findUnique: vi.fn() },
    permissionAction: { findMany: vi.fn() },
    rolePermission: { findFirst: vi.fn() },
    objectPermission: { findFirst: vi.fn() },
  },
}));

vi.mock("../db/prisma", () => ({ prisma: mockPrisma }));

import { canDo } from "../db/queries/canDo";

// ── fixtures ──────────────────────────────────────────────────────────────────

const ACTIONS = [
  { code: "read", id: "action-read" },
  { code: "write", id: "action-write" },
  { code: "delete", id: "action-delete" },
  { code: "manage_permissions", id: "action-manage" },
];

// Default role is an ordinary (non-admin) org role so canDo runs the full
// RBAC/OLP path; org_admin/trainer_admin short-circuit before those queries.
// userRoles carry orgId because canDo scopes roles to the requested org.
function activeUser(roleCode = "member", roleId = "role-1") {
  return {
    id: "user-1",
    isActive: true,
    userRoles: [{ roleId, orgId: "org-1", role: { code: roleCode } }],
  };
}

beforeEach(() => {
  vi.resetAllMocks();
  mockPrisma.permissionAction.findMany.mockResolvedValue(ACTIONS);
  mockPrisma.organization.findUnique.mockResolvedValue({ olpEnabled: false });
  mockPrisma.user.findUnique.mockResolvedValue(activeUser());
});

// ── permActionCache ───────────────────────────────────────────────────────────
// This describe runs first so the cache starts unpopulated.

describe("permActionCache", () => {
  test("calls permissionAction.findMany only once across multiple canDo calls", async () => {
    mockPrisma.rolePermission.findFirst.mockResolvedValue({ id: "rp-1" });

    await canDo("user-1", "read", "folder", "f1", "org-1");
    await canDo("user-1", "write", "folder", "f2", "org-1");

    expect(mockPrisma.permissionAction.findMany).toHaveBeenCalledTimes(1);
  });
});

// ── early-exit paths ──────────────────────────────────────────────────────────

describe("early exits (before DB permission checks)", () => {
  test("denies when orgId is null — no DB calls", async () => {
    const result = await canDo("user-1", "read", "folder", "f1", null);
    expect(result).toEqual({ allowed: false, reason: "no org context" });
    expect(mockPrisma.user.findUnique).not.toHaveBeenCalled();
  });

  test("denies when user not found", async () => {
    mockPrisma.user.findUnique.mockResolvedValueOnce(null);
    const result = await canDo("user-1", "read", "folder", "f1", "org-1");
    expect(result).toEqual({ allowed: false, reason: "user not found" });
  });

  test("denies when user is inactive", async () => {
    mockPrisma.user.findUnique.mockResolvedValueOnce({
      id: "user-1",
      isActive: false,
      userRoles: [],
    });
    const result = await canDo("user-1", "read", "folder", "f1", "org-1");
    expect(result).toEqual({ allowed: false, reason: "user not found" });
  });

  test("allows trainer_admin without org or permission queries", async () => {
    mockPrisma.user.findUnique.mockResolvedValueOnce(activeUser("trainer_admin"));
    const result = await canDo("user-1", "read", "folder", "f1", "org-1");
    expect(result).toEqual({ allowed: true, reason: "trainer_admin" });
    expect(mockPrisma.organization.findUnique).not.toHaveBeenCalled();
    expect(mockPrisma.rolePermission.findFirst).not.toHaveBeenCalled();
  });

  test("denies when action code is not in the permissionAction table", async () => {
    const result = await canDo("user-1", "read" as any, "folder", "f1", "org-1");
    // "read" IS in the cache from the permActionCache test above — use a bogus code
    // to confirm the null-from-cache path works
    const result2 = await canDo("user-1", "invalid_code" as any, "folder", "f1", "org-1");
    expect(result2).toEqual({ allowed: false, reason: "unknown action" });
  });
});

// ── RBAC path (OLP disabled) ──────────────────────────────────────────────────

describe("RBAC path (olpEnabled = false)", () => {
  test("allows when RBAC ceiling exists for the user's role", async () => {
    mockPrisma.rolePermission.findFirst.mockResolvedValueOnce({ id: "rp-1" });
    const result = await canDo("user-1", "read", "folder", "f1", "org-1");
    expect(result).toEqual({ allowed: true, reason: null });
  });

  test("denies when no RBAC ceiling", async () => {
    mockPrisma.rolePermission.findFirst.mockResolvedValueOnce(null);
    const result = await canDo("user-1", "read", "folder", "f1", "org-1");
    expect(result).toEqual({ allowed: false, reason: "no RBAC ceiling" });
  });

  test("queries rolePermission with correct actionId and resourceType", async () => {
    mockPrisma.rolePermission.findFirst.mockResolvedValueOnce({ id: "rp-1" });
    await canDo("user-1", "write", "folder", "f1", "org-1");
    expect(mockPrisma.rolePermission.findFirst).toHaveBeenCalledWith({
      where: {
        roleId: { in: ["role-1"] },
        actionId: "action-write",
        resourceType: "folder",
      },
    });
  });

  test("does not query objectPermission when OLP is disabled", async () => {
    mockPrisma.rolePermission.findFirst.mockResolvedValueOnce({ id: "rp-1" });
    await canDo("user-1", "read", "folder", "f1", "org-1");
    expect(mockPrisma.objectPermission.findFirst).not.toHaveBeenCalled();
  });
});

// ── OLP path (OLP enabled) ────────────────────────────────────────────────────

describe("OLP path (olpEnabled = true)", () => {
  beforeEach(() => {
    mockPrisma.organization.findUnique.mockResolvedValue({ olpEnabled: true });
  });

  test("allows when object permission grant exists", async () => {
    mockPrisma.objectPermission.findFirst.mockResolvedValueOnce({ id: "op-1" });
    const result = await canDo("user-1", "read", "folder", "f1", "org-1");
    expect(result).toEqual({ allowed: true, reason: null });
  });

  test("denies when no object permission grant", async () => {
    mockPrisma.objectPermission.findFirst.mockResolvedValueOnce(null);
    const result = await canDo("user-1", "read", "folder", "f1", "org-1");
    expect(result).toEqual({ allowed: false, reason: "no object permission" });
  });

  test("queries objectPermission with correct orgId, resourceType, resourceId, and actionId", async () => {
    mockPrisma.objectPermission.findFirst.mockResolvedValueOnce({ id: "op-1" });
    await canDo("user-1", "write", "folder", "folder-abc", "org-1");
    expect(mockPrisma.objectPermission.findFirst).toHaveBeenCalledWith({
      where: {
        orgId: "org-1",
        resourceType: "folder",
        resourceId: "folder-abc",
        actionId: "action-write",
        OR: [{ granteeUserId: "user-1" }, { granteeRoleId: { in: ["role-1"] } }],
      },
    });
  });

  test("does not query rolePermission when OLP is enabled", async () => {
    mockPrisma.objectPermission.findFirst.mockResolvedValueOnce({ id: "op-1" });
    await canDo("user-1", "read", "folder", "f1", "org-1");
    expect(mockPrisma.rolePermission.findFirst).not.toHaveBeenCalled();
  });
});
