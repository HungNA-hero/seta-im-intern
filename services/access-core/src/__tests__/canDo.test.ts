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
  delete process.env.TRAINER_ADMIN_ENABLED;
  delete process.env.TRAINER_ADMIN_EXPIRES_AT;
  vi.resetAllMocks();
  mockPrisma.permissionAction.findMany.mockResolvedValue(ACTIONS);
  mockPrisma.organization.findUnique.mockResolvedValue({ olpEnabled: false });
  mockPrisma.user.findUnique.mockResolvedValue(activeUser());
  mockPrisma.rolePermission.findFirst.mockResolvedValue(null);
  mockPrisma.objectPermission.findMany.mockResolvedValue([]);
  // No folder/metadata metadata by default — existing RBAC/OLP assertions
  // assume plain single-resource checks with no ancestor inheritance.
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
    const originalNodeEnv = process.env.NODE_ENV;
    process.env.NODE_ENV = "test";
    process.env.TRAINER_ADMIN_ENABLED = "true";
    process.env.TRAINER_ADMIN_EXPIRES_AT = "2099-01-01T00:00:00.000Z";
    try {
      mockPrisma.user.findUnique.mockResolvedValueOnce(activeUser("trainer_admin"));
      const result = await canDo("user-1", "read", "folder", "f1", "org-1");
      expect(result).toEqual({ allowed: true, reason: "trainer_admin" });
      expect(mockPrisma.organization.findUnique).not.toHaveBeenCalled();
      expect(mockPrisma.rolePermission.findFirst).not.toHaveBeenCalled();
    } finally {
      process.env.NODE_ENV = originalNodeEnv;
    }
  });

  test("never allows trainer_admin in production, even with an active temporary gate", async () => {
    const originalNodeEnv = process.env.NODE_ENV;
    process.env.NODE_ENV = "production";
    process.env.TRAINER_ADMIN_ENABLED = "true";
    process.env.TRAINER_ADMIN_EXPIRES_AT = "2099-01-01T00:00:00.000Z";
    try {
      mockPrisma.user.findUnique.mockResolvedValueOnce(activeUser("trainer_admin"));
      mockPrisma.rolePermission.findFirst.mockResolvedValueOnce(null);
      const result = await canDo("user-1", "read", "folder", "f1", "org-1");
      expect(result).toEqual({ allowed: false, reason: "no RBAC ceiling" });
    } finally {
      process.env.NODE_ENV = originalNodeEnv;
    }
  });

  test("denies expired trainer_admin access and falls through to ordinary evaluation", async () => {
    process.env.TRAINER_ADMIN_ENABLED = "true";
    process.env.TRAINER_ADMIN_EXPIRES_AT = "2000-01-01T00:00:00.000Z";
    mockPrisma.user.findUnique.mockResolvedValueOnce(activeUser("trainer_admin"));
    mockPrisma.rolePermission.findFirst.mockResolvedValueOnce(null);
    const result = await canDo("user-1", "read", "folder", "f1", "org-1");
    expect(result).toEqual({ allowed: false, reason: "no RBAC ceiling" });
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
  test("allows via RBAC ceiling alone, without querying object permissions", async () => {
    mockPrisma.rolePermission.findFirst.mockResolvedValue({ id: "rp-1" });
    const result = await canDo("user-1", "read", "folder", "f1", "org-1");
    expect(result).toEqual({ allowed: true, reason: null });
    expect(mockPrisma.objectPermission.findMany).not.toHaveBeenCalled();
  });

  test("denies with 'no RBAC ceiling' when no ceiling, without querying object permissions", async () => {
    mockPrisma.rolePermission.findFirst.mockResolvedValue(null);
    const result = await canDo("user-1", "read", "folder", "f1", "org-1");
    expect(result).toEqual({ allowed: false, reason: "no RBAC ceiling" });
    expect(mockPrisma.objectPermission.findMany).not.toHaveBeenCalled();
  });

  test("allows via ceiling even when no object grant exists at all", async () => {
    mockPrisma.rolePermission.findFirst.mockResolvedValue({ id: "rp-1" });
    mockPrisma.objectPermission.findMany.mockResolvedValue([]);
    const result = await canDo("user-1", "read", "folder", "f1", "org-1");
    expect(result).toEqual({ allowed: true, reason: null });
  });

  test("denies with 'no RBAC ceiling' when neither ceiling nor grant exists", async () => {
    const result = await canDo("user-1", "read", "folder", "f1", "org-1");
    expect(result).toEqual({ allowed: false, reason: "no RBAC ceiling" });
    expect(mockPrisma.objectPermission.findMany).not.toHaveBeenCalled();
  });

  test("queries rolePermission with correct actionId and resourceType", async () => {
    mockPrisma.rolePermission.findFirst.mockResolvedValue({ id: "rp-1" });
    await canDo("user-1", "write", "folder", "f1", "org-1");
    expect(mockPrisma.rolePermission.findFirst).toHaveBeenCalledWith({
      where: {
        roleId: { in: ["role-1"] },
        actionId: "action-write",
        resourceType: "folder",
      },
    });
  });

  test("checks the ceiling only, never queries object permissions, when ceiling exists", async () => {
    mockPrisma.rolePermission.findFirst.mockResolvedValue({ id: "rp-1" });
    await canDo("user-1", "read", "folder", "f1", "org-1");
    expect(mockPrisma.rolePermission.findFirst).toHaveBeenCalledTimes(1);
    expect(mockPrisma.objectPermission.findMany).not.toHaveBeenCalled();
    expect(mockGetFolderMeta).not.toHaveBeenCalled();
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

// ── creator status confers no bypass ───────────────────────────────────────────

describe("creator status confers no bypass", () => {
  test("a folder creator with no RBAC ceiling and no grant is still denied", async () => {
    mockGetFolderMeta.mockResolvedValue({ path: "abc" });
    mockPrisma.rolePermission.findFirst.mockResolvedValue(null);
    const result = await canDo("user-1", "read", "folder", "f1", "org-1");
    expect(result).toEqual({ allowed: false, reason: "no RBAC ceiling" });
  });

  test("a metadata item creator with no RBAC ceiling and no grant is still denied", async () => {
    mockGetMetadataMeta.mockResolvedValue({ folderId: "folder-1" });
    mockPrisma.rolePermission.findFirst.mockResolvedValue(null);
    const result = await canDo(
      "user-1",
      "read",
      "metadata_item",
      "m1",
      "org-1",
    );
    expect(result).toEqual({ allowed: false, reason: "no RBAC ceiling" });
  });

  test("a folder creator in OLP mode with RBAC ceiling but no grant is still denied", async () => {
    mockPrisma.organization.findUnique.mockResolvedValue({ olpEnabled: true });
    mockGetFolderMeta.mockResolvedValue({ path: "abc" });
    mockPrisma.rolePermission.findFirst.mockResolvedValue({ id: "rp-1" });
    mockPrisma.objectPermission.findMany.mockResolvedValue([]);
    const result = await canDo("user-1", "write", "folder", "f1", "org-1");
    expect(result).toEqual({ allowed: false, reason: "no object permission" });
  });

  test("a folder creator in OLP mode with a grant is allowed, same as any other grantee", async () => {
    mockPrisma.organization.findUnique.mockResolvedValue({ olpEnabled: true });
    mockGetFolderMeta.mockResolvedValue({ path: "abc" });
    mockPrisma.objectPermission.findMany.mockResolvedValue([
      { resourceId: "f1" },
    ]);
    const result = await canDo("user-1", "write", "folder", "f1", "org-1");
    expect(result).toEqual({ allowed: true, reason: null });
  });
});

// ── top-level folder creation (root sentinel) ─────────────────────────────────

describe("root folder creation (resourceId === orgId)", () => {
  test("allowed by RBAC ceiling alone in RBAC mode, without querying object grants", async () => {
    mockPrisma.rolePermission.findFirst.mockResolvedValue({ id: "rp-1" });
    const result = await canDo("user-1", "write", "folder", "org-1", "org-1");
    expect(result).toEqual({ allowed: true, reason: null });
    expect(mockGetFolderMeta).not.toHaveBeenCalled();
    expect(mockPrisma.objectPermission.findMany).not.toHaveBeenCalled();
  });

  test("denied when the role lacks the write ceiling on folder", async () => {
    mockPrisma.rolePermission.findFirst.mockResolvedValue(null);
    const result = await canDo("user-1", "write", "folder", "org-1", "org-1");
    expect(result).toEqual({ allowed: false, reason: "no RBAC ceiling" });
  });

  test("still allowed by RBAC ceiling in OLP mode, since no object grant can ever target the org root", async () => {
    mockPrisma.organization.findUnique.mockResolvedValue({ olpEnabled: true });
    mockPrisma.rolePermission.findFirst.mockResolvedValue({ id: "rp-1" });
    const result = await canDo("user-1", "write", "folder", "org-1", "org-1");
    expect(result).toEqual({ allowed: true, reason: null });
    expect(mockPrisma.objectPermission.findMany).not.toHaveBeenCalled();
  });

  test("denied in OLP mode when the role lacks the write ceiling on folder", async () => {
    mockPrisma.organization.findUnique.mockResolvedValue({ olpEnabled: true });
    mockPrisma.rolePermission.findFirst.mockResolvedValue(null);
    const result = await canDo("user-1", "write", "folder", "org-1", "org-1");
    expect(result).toEqual({ allowed: false, reason: "no RBAC ceiling" });
  });
});

// ── folder ancestor inheritance ───────────────────────────────────────────────

// Ancestor-grant inheritance is an OLP-only concern: RBAC mode is decided by
// ceiling alone and never looks at grants at all (see "RBAC path" above).
describe("folder ancestor inheritance (OLP mode)", () => {
  beforeEach(() => {
    mockPrisma.organization.findUnique.mockResolvedValue({ olpEnabled: true });
  });

  test("a grant on an ancestor folder satisfies canDo for a descendant", async () => {
    const rootId = "11111111-1111-1111-1111-111111111111";
    const childId = "22222222-2222-2222-2222-222222222222";
    mockGetFolderMeta.mockResolvedValue({
      path: `${rootId.replace(/-/g, "")}.${childId.replace(/-/g, "")}`,
    });
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
      path: `${rootId.replace(/-/g, "")}.${childId.replace(/-/g, "")}`,
    });
    mockPrisma.objectPermission.findMany.mockResolvedValue([]);

    const result = await canDo("user-1", "read", "folder", childId, "org-1");
    expect(result).toEqual({ allowed: false, reason: "no object permission" });
  });
});

// ── metadata inherits from folder (OLP mode) ──────────────────────────────────

describe("metadata inherits from folder (OLP mode)", () => {
  beforeEach(() => {
    mockPrisma.organization.findUnique.mockResolvedValue({ olpEnabled: true });
  });

  test("a grant on the containing folder satisfies canDo for a metadata item", async () => {
    const folderId = "11111111-1111-1111-1111-111111111111";
    mockGetMetadataMeta.mockResolvedValue({ folderId });
    mockGetFolderMeta.mockResolvedValue({
      path: folderId.replace(/-/g, ""),
    });
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
    mockGetMetadataMeta.mockResolvedValue({ folderId });
    mockGetFolderMeta.mockResolvedValue({
      path: `${rootId.replace(/-/g, "")}.${folderId.replace(/-/g, "")}`,
    });
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
    mockGetMetadataMeta.mockResolvedValue({ folderId });
    mockGetFolderMeta.mockResolvedValue({
      path: folderId.replace(/-/g, ""),
    });
    mockPrisma.objectPermission.findMany.mockResolvedValue([]);

    const result = await canDo("user-1", "read", "metadata_item", "m1", "org-1");
    expect(result).toEqual({ allowed: false, reason: "no object permission" });
  });
});

// ── manage_permissions never inherits (OLP mode) ──────────────────────────────
// Unlike read/write/delete, a manage_permissions grant lets the grantee
// create further grants — letting it cascade down a tree would silently hand
// out permission-management authority over content the grantor never
// explicitly covered. It must only ever apply to the exact resource granted.

describe("manage_permissions never inherits (OLP mode)", () => {
  beforeEach(() => {
    mockPrisma.organization.findUnique.mockResolvedValue({ olpEnabled: true });
  });

  test("a grant on an ancestor folder does NOT satisfy manage_permissions for a descendant", async () => {
    const rootId = "11111111-1111-1111-1111-111111111111";
    const childId = "22222222-2222-2222-2222-222222222222";
    // Simulates the real DB: a grant exists for rootId, but the query below
    // must only ever ask for childId (never rootId as an inherited ancestor).
    mockPrisma.objectPermission.findMany.mockImplementation(
      async ({ where }: { where: { resourceId: { in: string[] } } }) =>
        where.resourceId.in.includes(rootId)
          ? [{ resourceId: rootId }]
          : [],
    );

    const result = await canDo(
      "user-1",
      "manage_permissions",
      "folder",
      childId,
      "org-1",
    );
    expect(result).toEqual({ allowed: false, reason: "no object permission" });
    expect(mockGetFolderMeta).not.toHaveBeenCalled();
    expect(mockPrisma.objectPermission.findMany).toHaveBeenCalledWith(
      expect.objectContaining({
        where: expect.objectContaining({ resourceId: { in: [childId] } }),
      }),
    );
  });

  test("a grant on the exact folder still satisfies manage_permissions", async () => {
    const folderId = "33333333-3333-3333-3333-333333333333";
    mockPrisma.objectPermission.findMany.mockResolvedValue([{ resourceId: folderId }]);

    const result = await canDo(
      "user-1",
      "manage_permissions",
      "folder",
      folderId,
      "org-1",
    );
    expect(result).toEqual({ allowed: true, reason: null });
  });

  test("a grant on the containing folder does NOT satisfy manage_permissions for a metadata item", async () => {
    const folderId = "11111111-1111-1111-1111-111111111111";
    mockPrisma.objectPermission.findMany.mockResolvedValue([]); // no direct grant on m1

    const result = await canDo(
      "user-1",
      "manage_permissions",
      "metadata_item",
      "m1",
      "org-1",
    );
    expect(result).toEqual({ allowed: false, reason: "no object permission" });
    expect(mockGetMetadataMeta).not.toHaveBeenCalled();
    expect(mockGetFolderMeta).not.toHaveBeenCalled();
  });

  test("a direct grant on the metadata item still satisfies manage_permissions", async () => {
    mockPrisma.objectPermission.findMany.mockResolvedValue([{ resourceId: "m1" }]);

    const result = await canDo(
      "user-1",
      "manage_permissions",
      "metadata_item",
      "m1",
      "org-1",
    );
    expect(result).toEqual({ allowed: true, reason: null });
  });

  test("filterAllowedResourceIds does not fall back to ancestor grants for manage_permissions", async () => {
    mockPrisma.objectPermission.findMany.mockResolvedValue([]);
    const result = await filterAllowedResourceIds(
      "user-1",
      "org-1",
      "manage_permissions",
      "folder",
      ["child-1"],
      [{ id: "child-1" }],
      () => ({ ancestorIds: ["ancestor-1"] }),
    );
    expect(result).toEqual(new Set());
    expect(mockPrisma.objectPermission.findMany).toHaveBeenCalledTimes(1);
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
    const originalNodeEnv = process.env.NODE_ENV;
    process.env.NODE_ENV = "test";
    process.env.TRAINER_ADMIN_ENABLED = "true";
    process.env.TRAINER_ADMIN_EXPIRES_AT = "2099-01-01T00:00:00.000Z";
    try {
      mockPrisma.user.findUnique.mockResolvedValueOnce(activeUser("trainer_admin"));
      const result = await filterAllowedResourceIds("user-1", "org-1", "read", "folder", ["f1", "f2"]);
      expect(result).toEqual(new Set(["f1", "f2"]));
      expect(mockPrisma.rolePermission.findFirst).not.toHaveBeenCalled();
    } finally {
      process.env.NODE_ENV = originalNodeEnv;
    }
    expect(mockPrisma.objectPermission.findMany).not.toHaveBeenCalled();
  });

  test("org_admin gets all IDs without querying ceiling or grants", async () => {
    mockPrisma.user.findUnique.mockResolvedValueOnce(activeUser("org_admin"));
    const result = await filterAllowedResourceIds("user-1", "org-1", "read", "folder", ["f1", "f2"]);
    expect(result).toEqual(new Set(["f1", "f2"]));
    expect(mockPrisma.rolePermission.findFirst).not.toHaveBeenCalled();
    expect(mockPrisma.objectPermission.findMany).not.toHaveBeenCalled();
  });

  test("RBAC mode: returns all IDs when ceiling exists, without querying grants", async () => {
    mockPrisma.organization.findUnique.mockResolvedValue({ olpEnabled: false });
    mockPrisma.rolePermission.findFirst.mockResolvedValue({ id: "rp-1" });
    const result = await filterAllowedResourceIds("user-1", "org-1", "read", "folder", ["f1", "f2"]);
    expect(result).toEqual(new Set(["f1", "f2"]));
    expect(mockPrisma.objectPermission.findMany).not.toHaveBeenCalled();
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
