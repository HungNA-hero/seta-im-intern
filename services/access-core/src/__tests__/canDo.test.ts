import { describe, test, expect, vi, beforeEach } from "vitest";

const { mockPrisma } = vi.hoisted(() => ({
  mockPrisma: {
    user: { findUnique: vi.fn() },
    organization: { findUnique: vi.fn() },
    permissionAction: { findMany: vi.fn() },
    rolePermission: { findFirst: vi.fn() },
    objectPermission: { findMany: vi.fn() },
  },
}));

const { mockGetFolderMeta, mockGetMetadataMeta } = vi.hoisted(() => ({
  mockGetFolderMeta: vi.fn(),
  mockGetMetadataMeta: vi.fn(),
}));

vi.mock("../db/prisma", () => ({ prisma: mockPrisma }));
vi.mock("../clients/assetClient", () => ({
  getFolderMeta: mockGetFolderMeta,
  getMetadataMeta: mockGetMetadataMeta,
}));

import { canDo, filterAllowedResourceIds } from "../db/queries/canDo";

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
  mockPrisma.rolePermission.findFirst.mockResolvedValue(null);
  mockPrisma.objectPermission.findMany.mockResolvedValue([]);
  // No owner/hierarchy data by default — existing RBAC/OLP assertions assume
  // plain single-resource checks with no owner-bypass or inheritance.
  mockGetFolderMeta.mockResolvedValue(null);
  mockGetMetadataMeta.mockResolvedValue(null);
});

// ── permActionCache ───────────────────────────────────────────────────────────
// This describe runs first so the cache starts unpopulated.

describe("permActionCache", () => {
  test("calls permissionAction.findMany only once across multiple canDo calls", async () => {
    mockPrisma.rolePermission.findFirst.mockResolvedValue({ id: "rp-1" });
    mockPrisma.objectPermission.findMany.mockResolvedValue([{ resourceId: "f1" }]);

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
  test("allows when RBAC ceiling AND object grant both exist", async () => {
    mockPrisma.rolePermission.findFirst.mockResolvedValue({ id: "rp-1" });
    mockPrisma.objectPermission.findMany.mockResolvedValue([{ resourceId: "f1" }]);
    const result = await canDo("user-1", "read", "folder", "f1", "org-1");
    expect(result).toEqual({ allowed: true, reason: null });
  });

  test("denies with 'no RBAC ceiling' when no ceiling, without querying object permissions", async () => {
    mockPrisma.rolePermission.findFirst.mockResolvedValue(null);
    const result = await canDo("user-1", "read", "folder", "f1", "org-1");
    expect(result).toEqual({ allowed: false, reason: "no RBAC ceiling" });
    expect(mockPrisma.objectPermission.findMany).not.toHaveBeenCalled();
  });

  test("denies with 'no object permission' when ceiling exists but no grant", async () => {
    mockPrisma.rolePermission.findFirst.mockResolvedValue({ id: "rp-1" });
    mockPrisma.objectPermission.findMany.mockResolvedValue([]);
    const result = await canDo("user-1", "read", "folder", "f1", "org-1");
    expect(result).toEqual({ allowed: false, reason: "no object permission" });
  });

  test("denies with 'no RBAC ceiling' when neither ceiling nor grant exists", async () => {
    const result = await canDo("user-1", "read", "folder", "f1", "org-1");
    expect(result).toEqual({ allowed: false, reason: "no RBAC ceiling" });
    expect(mockPrisma.objectPermission.findMany).not.toHaveBeenCalled();
  });

  test("queries rolePermission with correct actionId and resourceType", async () => {
    mockPrisma.rolePermission.findFirst.mockResolvedValue({ id: "rp-1" });
    mockPrisma.objectPermission.findMany.mockResolvedValue([{ resourceId: "f1" }]);
    await canDo("user-1", "write", "folder", "f1", "org-1");
    expect(mockPrisma.rolePermission.findFirst).toHaveBeenCalledWith({
      where: {
        roleId: { in: ["role-1"] },
        actionId: "action-write",
        resourceType: "folder",
      },
    });
  });

  test("queries objectPermission with correct fields in RBAC mode", async () => {
    mockPrisma.rolePermission.findFirst.mockResolvedValue({ id: "rp-1" });
    mockPrisma.objectPermission.findMany.mockResolvedValue([{ resourceId: "folder-abc" }]);
    await canDo("user-1", "write", "folder", "folder-abc", "org-1");
    expect(mockPrisma.objectPermission.findMany).toHaveBeenCalledWith({
      where: {
        orgId: "org-1",
        resourceType: "folder",
        actionId: "action-write",
        resourceId: { in: ["folder-abc"] },
        OR: [{ granteeUserId: "user-1" }, { granteeRoleId: { in: ["role-1"] } }],
      },
      select: { resourceId: true },
    });
  });

  test("checks the ceiling before querying object permissions, once each, when ceiling exists", async () => {
    mockPrisma.rolePermission.findFirst.mockResolvedValue({ id: "rp-1" });
    mockPrisma.objectPermission.findMany.mockResolvedValue([{ resourceId: "f1" }]);
    await canDo("user-1", "read", "folder", "f1", "org-1");
    expect(mockPrisma.rolePermission.findFirst).toHaveBeenCalledTimes(1);
    expect(mockPrisma.objectPermission.findMany).toHaveBeenCalledTimes(1);
  });
});

// ── OLP path (OLP enabled) ────────────────────────────────────────────────────

describe("OLP path (olpEnabled = true)", () => {
  beforeEach(() => {
    mockPrisma.organization.findUnique.mockResolvedValue({ olpEnabled: true });
  });

  test("allows when object permission grant exists", async () => {
    mockPrisma.objectPermission.findMany.mockResolvedValue([{ resourceId: "f1" }]);
    const result = await canDo("user-1", "read", "folder", "f1", "org-1");
    expect(result).toEqual({ allowed: true, reason: null });
  });

  test("allows when grant exists even without RBAC ceiling, without querying the ceiling", async () => {
    mockPrisma.rolePermission.findFirst.mockResolvedValue(null);
    mockPrisma.objectPermission.findMany.mockResolvedValue([{ resourceId: "f1" }]);
    const result = await canDo("user-1", "read", "folder", "f1", "org-1");
    expect(result).toEqual({ allowed: true, reason: null });
    expect(mockPrisma.rolePermission.findFirst).not.toHaveBeenCalled();
  });

  test("denies with 'no object permission' when no grant, ceiling irrelevant", async () => {
    mockPrisma.rolePermission.findFirst.mockResolvedValue({ id: "rp-1" });
    mockPrisma.objectPermission.findMany.mockResolvedValue([]);
    const result = await canDo("user-1", "read", "folder", "f1", "org-1");
    expect(result).toEqual({ allowed: false, reason: "no object permission" });
  });

  test("denies with 'no object permission' when no grant and no ceiling", async () => {
    const result = await canDo("user-1", "read", "folder", "f1", "org-1");
    expect(result).toEqual({ allowed: false, reason: "no object permission" });
  });

  test("queries objectPermission with correct orgId, resourceType, resourceId, and actionId", async () => {
    mockPrisma.objectPermission.findMany.mockResolvedValue([{ resourceId: "folder-abc" }]);
    await canDo("user-1", "write", "folder", "folder-abc", "org-1");
    expect(mockPrisma.objectPermission.findMany).toHaveBeenCalledWith({
      where: {
        orgId: "org-1",
        resourceType: "folder",
        actionId: "action-write",
        resourceId: { in: ["folder-abc"] },
        OR: [{ granteeUserId: "user-1" }, { granteeRoleId: { in: ["role-1"] } }],
      },
      select: { resourceId: true },
    });
  });

  test("queries objectPermission only, skipping the RBAC ceiling entirely", async () => {
    mockPrisma.objectPermission.findMany.mockResolvedValue([{ resourceId: "f1" }]);
    await canDo("user-1", "read", "folder", "f1", "org-1");
    expect(mockPrisma.rolePermission.findFirst).not.toHaveBeenCalled();
    expect(mockPrisma.objectPermission.findMany).toHaveBeenCalledTimes(1);
  });
});

// ── owner bypass ──────────────────────────────────────────────────────────────

describe("owner bypass", () => {
  test("allows the folder creator without RBAC ceiling or grant", async () => {
    mockGetFolderMeta.mockResolvedValue({ createdBy: "user-1", path: "abc" });
    const result = await canDo("user-1", "read", "folder", "f1", "org-1");
    expect(result).toEqual({ allowed: true, reason: "owner" });
    expect(mockPrisma.rolePermission.findFirst).not.toHaveBeenCalled();
    expect(mockPrisma.objectPermission.findMany).not.toHaveBeenCalled();
  });

  test("allows the metadata item creator without RBAC ceiling or grant", async () => {
    mockGetMetadataMeta.mockResolvedValue({
      createdBy: "user-1",
      folderId: "folder-1",
    });
    const result = await canDo("user-1", "read", "metadata_item", "m1", "org-1");
    expect(result).toEqual({ allowed: true, reason: "owner" });
    expect(mockPrisma.rolePermission.findFirst).not.toHaveBeenCalled();
    expect(mockPrisma.objectPermission.findMany).not.toHaveBeenCalled();
  });

  test("does not bypass when the requesting user is not the creator", async () => {
    mockGetFolderMeta.mockResolvedValue({ createdBy: "someone-else", path: "abc" });
    mockPrisma.rolePermission.findFirst.mockResolvedValue(null);
    const result = await canDo("user-1", "read", "folder", "f1", "org-1");
    expect(result).toEqual({ allowed: false, reason: "no RBAC ceiling" });
  });
});

// ── folder ancestor inheritance ───────────────────────────────────────────────

describe("folder ancestor inheritance", () => {
  test("a grant on an ancestor folder satisfies canDo for a descendant", async () => {
    const rootId = "11111111-1111-1111-1111-111111111111";
    const childId = "22222222-2222-2222-2222-222222222222";
    mockGetFolderMeta.mockResolvedValue({
      createdBy: "someone-else",
      path: `${rootId.replace(/-/g, "")}.${childId.replace(/-/g, "")}`,
    });
    mockPrisma.rolePermission.findFirst.mockResolvedValue({ id: "rp-1" });
    mockPrisma.objectPermission.findMany.mockResolvedValue([{ resourceId: rootId }]);

    const result = await canDo("user-1", "read", "folder", childId, "org-1");
    expect(result).toEqual({ allowed: true, reason: null });
    expect(mockPrisma.objectPermission.findMany).toHaveBeenCalledWith(
      expect.objectContaining({
        where: expect.objectContaining({
          resourceId: { in: [childId, rootId] },
        }),
      }),
    );
  });

  test("denies when neither the folder nor any ancestor has a grant", async () => {
    const rootId = "11111111-1111-1111-1111-111111111111";
    const childId = "22222222-2222-2222-2222-222222222222";
    mockGetFolderMeta.mockResolvedValue({
      createdBy: "someone-else",
      path: `${rootId.replace(/-/g, "")}.${childId.replace(/-/g, "")}`,
    });
    mockPrisma.rolePermission.findFirst.mockResolvedValue({ id: "rp-1" });
    mockPrisma.objectPermission.findMany.mockResolvedValue([]);

    const result = await canDo("user-1", "read", "folder", childId, "org-1");
    expect(result).toEqual({ allowed: false, reason: "no object permission" });
  });
});

// ── metadata inherits from folder ─────────────────────────────────────────────

describe("metadata inherits from folder", () => {
  test("a grant on the containing folder satisfies canDo for a metadata item", async () => {
    const folderId = "11111111-1111-1111-1111-111111111111";
    mockGetMetadataMeta.mockResolvedValue({ createdBy: "someone-else", folderId });
    mockGetFolderMeta.mockResolvedValue({
      createdBy: "someone-else",
      path: folderId.replace(/-/g, ""),
    });
    mockPrisma.rolePermission.findFirst.mockResolvedValue({ id: "rp-1" });
    mockPrisma.objectPermission.findMany
      .mockResolvedValueOnce([]) // direct metadata_item grant check
      .mockResolvedValueOnce([{ resourceId: folderId }]); // folder grant check

    const result = await canDo("user-1", "read", "metadata_item", "m1", "org-1");
    expect(result).toEqual({ allowed: true, reason: null });
    expect(mockGetFolderMeta).toHaveBeenCalledWith("org-1", "user-1", folderId);
  });

  test("a grant on an ancestor of the containing folder satisfies canDo for a metadata item", async () => {
    const rootId = "11111111-1111-1111-1111-111111111111";
    const folderId = "22222222-2222-2222-2222-222222222222";
    mockGetMetadataMeta.mockResolvedValue({ createdBy: "someone-else", folderId });
    mockGetFolderMeta.mockResolvedValue({
      createdBy: "someone-else",
      path: `${rootId.replace(/-/g, "")}.${folderId.replace(/-/g, "")}`,
    });
    mockPrisma.rolePermission.findFirst.mockResolvedValue({ id: "rp-1" });
    mockPrisma.objectPermission.findMany
      .mockResolvedValueOnce([]) // direct metadata_item grant check
      .mockResolvedValueOnce([{ resourceId: rootId }]); // folder grant check (on the ancestor)

    const result = await canDo("user-1", "read", "metadata_item", "m1", "org-1");
    expect(result).toEqual({ allowed: true, reason: null });
    expect(mockPrisma.objectPermission.findMany).toHaveBeenLastCalledWith(
      expect.objectContaining({
        where: expect.objectContaining({
          resourceType: "folder",
          resourceId: { in: [folderId, rootId] },
        }),
      }),
    );
  });

  test("denies when neither the item nor its folder has a grant", async () => {
    const folderId = "11111111-1111-1111-1111-111111111111";
    mockGetMetadataMeta.mockResolvedValue({ createdBy: "someone-else", folderId });
    mockGetFolderMeta.mockResolvedValue({
      createdBy: "someone-else",
      path: folderId.replace(/-/g, ""),
    });
    mockPrisma.rolePermission.findFirst.mockResolvedValue({ id: "rp-1" });
    mockPrisma.objectPermission.findMany.mockResolvedValue([]);

    const result = await canDo("user-1", "read", "metadata_item", "m1", "org-1");
    expect(result).toEqual({ allowed: false, reason: "no object permission" });
  });
});

// ── filterAllowedResourceIds ──────────────────────────────────────────────────

describe("filterAllowedResourceIds", () => {
  test("returns empty set immediately for empty resourceIds — no DB calls", async () => {
    const result = await filterAllowedResourceIds("user-1", "org-1", "read", "folder", []);
    expect(result).toEqual(new Set());
    expect(mockPrisma.user.findUnique).not.toHaveBeenCalled();
  });

  test("trainer_admin gets all IDs without querying ceiling or grants", async () => {
    mockPrisma.user.findUnique.mockResolvedValueOnce(activeUser("trainer_admin"));
    const result = await filterAllowedResourceIds("user-1", "org-1", "read", "folder", ["f1", "f2"]);
    expect(result).toEqual(new Set(["f1", "f2"]));
    expect(mockPrisma.rolePermission.findFirst).not.toHaveBeenCalled();
    expect(mockPrisma.objectPermission.findMany).not.toHaveBeenCalled();
  });

  test("org_admin gets all IDs without querying ceiling or grants", async () => {
    mockPrisma.user.findUnique.mockResolvedValueOnce(activeUser("org_admin"));
    const result = await filterAllowedResourceIds("user-1", "org-1", "read", "folder", ["f1", "f2"]);
    expect(result).toEqual(new Set(["f1", "f2"]));
    expect(mockPrisma.rolePermission.findFirst).not.toHaveBeenCalled();
    expect(mockPrisma.objectPermission.findMany).not.toHaveBeenCalled();
  });

  test("RBAC mode: returns only granted IDs when ceiling exists", async () => {
    mockPrisma.organization.findUnique.mockResolvedValue({ olpEnabled: false });
    mockPrisma.rolePermission.findFirst.mockResolvedValue({ id: "rp-1" });
    mockPrisma.objectPermission.findMany.mockResolvedValue([{ resourceId: "f1" }]);
    const result = await filterAllowedResourceIds("user-1", "org-1", "read", "folder", ["f1", "f2"]);
    expect(result).toEqual(new Set(["f1"]));
  });

  test("RBAC mode: returns empty set when no ceiling, without querying grants", async () => {
    mockPrisma.organization.findUnique.mockResolvedValue({ olpEnabled: false });
    mockPrisma.rolePermission.findFirst.mockResolvedValue(null);
    const result = await filterAllowedResourceIds("user-1", "org-1", "read", "folder", ["f1", "f2"]);
    expect(result).toEqual(new Set());
    expect(mockPrisma.objectPermission.findMany).not.toHaveBeenCalled();
  });

  test("OLP mode: returns granted IDs without querying the ceiling", async () => {
    mockPrisma.organization.findUnique.mockResolvedValue({ olpEnabled: true });
    mockPrisma.objectPermission.findMany.mockResolvedValue([{ resourceId: "f2" }]);
    const result = await filterAllowedResourceIds("user-1", "org-1", "read", "folder", ["f1", "f2"]);
    expect(result).toEqual(new Set(["f2"]));
    expect(mockPrisma.rolePermission.findFirst).not.toHaveBeenCalled();
  });
});
