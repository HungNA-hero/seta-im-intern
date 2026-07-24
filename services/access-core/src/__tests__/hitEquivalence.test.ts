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

describe.skipIf(!liveRedis)("canDo warm-hit outcome equivalence (live Redis)", () => {
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
    mockPrisma.organization.findUnique.mockResolvedValue({ olpEnabled: false });
    mockPrisma.rolePermission.findFirst.mockResolvedValue({ id: "grant-1" });

    const { flushAllForTests } = await import("./helpers/redisTestUtils");
    await flushAllForTests();
  });

  afterAll(async () => {
    const { closeRedisClient } = await import("../cache/redisClient");
    await closeRedisClient();
  });

  test("second identical decision hits the cache and matches the first outcome", async () => {
    const { canDo } = await import("../authz/decision");
    const { getMetricsSnapshotForTests } = await import("../cache/metrics");

    const first = await canDo(userId, "read", "folder", folderId, orgId);
    expect(first).toEqual({ allowed: true, reason: null });
    expect(mockPrisma.rolePermission.findFirst).toHaveBeenCalledTimes(1);

    const before = getMetricsSnapshotForTests().counters.decision_hit;
    const second = await canDo(userId, "read", "folder", folderId, orgId);
    const after = getMetricsSnapshotForTests().counters.decision_hit;

    expect(second).toEqual(first);
    expect(after).toBe(before + 1);
    expect(mockPrisma.rolePermission.findFirst).toHaveBeenCalledTimes(1);
  });
});
