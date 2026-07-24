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

describe.skipIf(!liveRedis)("direct revoke invalidates a cached decision (live Redis)", () => {
  const orgId = randomUUID();
  const userId = randomUUID();
  const folderId = randomUUID();
  const roleId = randomUUID();

  beforeEach(async () => {
    vi.clearAllMocks();
    mockPrisma.permissionAction.findMany.mockResolvedValue([
      { code: "read", id: "action-read" },
    ]);
    mockPrisma.user.findUnique.mockResolvedValue({
      id: userId,
      isActive: true,
      userRoles: [{ roleId, orgId, role: { code: "member" } }],
    });
    mockPrisma.organization.findUnique.mockResolvedValue({ olpEnabled: true });

    const { flushAllForTests } = await import("./helpers/redisTestUtils");
    await flushAllForTests();
  });

  afterAll(async () => {
    const { closeRedisClient } = await import("../cache/redisClient");
    await closeRedisClient();
  });

  test("bumping epoch:user after a direct revoke stops access within the same process", async () => {
    const { canDo } = await import("../authz/decision");
    const { bumpUserEpoch } = await import("../cache/epoch");

    mockPrisma.objectPermission.findMany.mockResolvedValue([{ resourceId: folderId }]);
    const granted = await canDo(userId, "read", "folder", folderId, orgId);
    expect(granted).toEqual({ allowed: true, reason: null });

    // Simulate the revoke: DB row removed, epoch bumped the way
    // permissionResolvers.ts does after the commit.
    mockPrisma.objectPermission.findMany.mockResolvedValue([]);
    await bumpUserEpoch(orgId, userId);

    const denied = await canDo(userId, "read", "folder", folderId, orgId);
    expect(denied).toEqual({ allowed: false, reason: "no object permission" });
  });
});
