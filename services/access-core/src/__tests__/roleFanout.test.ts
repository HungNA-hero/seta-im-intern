import { describe, test, expect, vi, beforeEach, afterAll } from "vitest";
import { randomUUID } from "node:crypto";

const { mockPrisma } = vi.hoisted(() => ({
  mockPrisma: {
    user: { findUnique: vi.fn() },
    organization: { findUnique: vi.fn() },
    permissionAction: { findMany: vi.fn() },
    rolePermission: { findFirst: vi.fn() },
    objectPermission: { findMany: vi.fn() },
  },
}));

vi.mock("../db/prisma", () => ({ prisma: mockPrisma }));
vi.mock("../clients/assetClient", () => ({
  getFolderMeta: vi.fn().mockResolvedValue(null),
  getMetadataMeta: vi.fn().mockResolvedValue(null),
}));

const liveRedis = process.env.ACCESS_REDIS_LIVE_TEST === "1";

describe.skipIf(!liveRedis)("role-directed revoke fans out to every holder (live Redis)", () => {
  const orgId = randomUUID();
  const roleId = randomUUID();
  const folderId = randomUUID();
  const userA = randomUUID();
  const userB = randomUUID();

  function mockUser(userId: string) {
    mockPrisma.user.findUnique.mockResolvedValue({
      id: userId,
      isActive: true,
      userRoles: [{ roleId, orgId, role: { code: "member" } }],
    });
  }

  beforeEach(async () => {
    vi.clearAllMocks();
    mockPrisma.permissionAction.findMany.mockResolvedValue([
      { code: "read", id: "action-read" },
    ]);
    mockPrisma.organization.findUnique.mockResolvedValue({ olpEnabled: true });
    mockPrisma.objectPermission.findMany.mockResolvedValue([{ resourceId: folderId }]);

    const { flushAllForTests } = await import("./helpers/redisTestUtils");
    await flushAllForTests();
  });

  afterAll(async () => {
    const { closeRedisClient } = await import("../cache/redisClient");
    await closeRedisClient();
  });

  test("a single epoch:role bump invalidates cached decisions for every holder", async () => {
    const { canDo } = await import("../authz/decision");
    const { bumpRoleEpoch } = await import("../cache/epoch");

    mockUser(userA);
    const grantedA = await canDo(userA, "read", "folder", folderId, orgId);
    expect(grantedA).toEqual({ allowed: true, reason: null });

    mockUser(userB);
    const grantedB = await canDo(userB, "read", "folder", folderId, orgId);
    expect(grantedB).toEqual({ allowed: true, reason: null });

    // Simulate a role-directed revoke: the grant row for the role is gone,
    // and the resolver bumps epoch:role once for all holders.
    mockPrisma.objectPermission.findMany.mockResolvedValue([]);
    await bumpRoleEpoch(orgId, roleId);

    mockUser(userA);
    const deniedA = await canDo(userA, "read", "folder", folderId, orgId);
    expect(deniedA).toEqual({ allowed: false, reason: "no object permission" });

    mockUser(userB);
    const deniedB = await canDo(userB, "read", "folder", folderId, orgId);
    expect(deniedB).toEqual({ allowed: false, reason: "no object permission" });
  });
});
