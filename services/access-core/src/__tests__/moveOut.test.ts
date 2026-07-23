import { describe, test, expect, vi, beforeEach, afterAll } from "vitest";
import { randomUUID } from "node:crypto";

const { mockPrisma, getFolderMeta } = vi.hoisted(() => ({
  mockPrisma: {
    user: { findUnique: vi.fn() },
    organization: { findUnique: vi.fn() },
    permissionAction: { findMany: vi.fn() },
    rolePermission: { findFirst: vi.fn() },
    objectPermission: { findMany: vi.fn() },
  },
  getFolderMeta: vi.fn(),
}));

vi.mock("../db/prisma", () => ({ prisma: mockPrisma }));
vi.mock("../clients/assetClient", () => ({
  getFolderMeta,
  getMetadataMeta: vi.fn().mockResolvedValue(null),
}));

const liveRedis = process.env.ACCESS_REDIS_LIVE_TEST === "1";

function noDash(id: string): string {
  return id.replace(/-/g, "");
}

describe.skipIf(!liveRedis)("folder move-out invalidates inherited decisions (live Redis)", () => {
  const orgId = randomUUID();
  const userId = randomUUID();
  const roleId = randomUUID();
  const grantedAncestorId = randomUUID();
  const childFolderId = randomUUID();

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
    // Never a direct grant on the child; only ever inherited from the ancestor.
    mockPrisma.objectPermission.findMany.mockImplementation(
      async ({ where }: { where: { resourceId: { in: string[] } } }) => {
        const ids = where.resourceId.in;
        return ids.includes(grantedAncestorId) ? [{ resourceId: grantedAncestorId }] : [];
      },
    );

    const { flushAllForTests } = await import("./helpers/redisTestUtils");
    await flushAllForTests();
  });

  afterAll(async () => {
    const { closeRedisClient } = await import("../cache/redisClient");
    await closeRedisClient();
  });

  test("bumping epoch:asset after a move stops inheriting from the old ancestor", async () => {
    const { canDo } = await import("../authz/decision");
    const { bumpAssetEpoch } = await import("../cache/epoch");

    getFolderMeta.mockResolvedValue({
      path: `${noDash(grantedAncestorId)}.${noDash(childFolderId)}`,
    });
    const grantedBeforeMove = await canDo(userId, "read", "folder", childFolderId, orgId);
    expect(grantedBeforeMove).toEqual({ allowed: true, reason: null });

    // Simulate the move: the child's path no longer descends from the
    // granted ancestor, and asset-core bumps epoch:asset after commit.
    getFolderMeta.mockResolvedValue({ path: noDash(childFolderId) });
    await bumpAssetEpoch(orgId);

    const deniedAfterMove = await canDo(userId, "read", "folder", childFolderId, orgId);
    expect(deniedAfterMove).toEqual({ allowed: false, reason: "no object permission" });
  });
});
