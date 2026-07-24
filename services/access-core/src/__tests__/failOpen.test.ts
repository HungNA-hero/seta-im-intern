import { describe, test, expect, vi, beforeEach, afterEach } from "vitest";
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

function brokenRedisClient() {
  const fail = () => Promise.reject(new Error("ECONNREFUSED: redis is down"));
  return {
    get: fail,
    set: fail,
    incr: fail,
    eval: fail,
  } as any;
}

describe("Redis-down fail-open behavior", () => {
  const orgId = randomUUID();
  const userId = randomUUID();
  const folderId = randomUUID();
  const roleId = randomUUID();

  beforeEach(async () => {
    vi.clearAllMocks();
    vi.resetModules();
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
  });

  afterEach(async () => {
    const { setRedisClientForTests } = await import("../cache/redisClient");
    setRedisClientForTests(null);
  });

  test("canDo still returns the authoritative decision when every Redis call fails", async () => {
    const { setRedisClientForTests } = await import("../cache/redisClient");
    setRedisClientForTests(brokenRedisClient());
    const { resetCircuitBreakerForTests } = await import("../cache/failOpen");
    resetCircuitBreakerForTests();

    const { canDo } = await import("../authz/decision");
    const result = await canDo(userId, "read", "folder", folderId, orgId);

    expect(result).toEqual({ allowed: true, reason: null });
  });

  test("never resolves a Redis failure to a permissive default when the DB denies", async () => {
    const { setRedisClientForTests } = await import("../cache/redisClient");
    setRedisClientForTests(brokenRedisClient());
    const { resetCircuitBreakerForTests } = await import("../cache/failOpen");
    resetCircuitBreakerForTests();

    mockPrisma.rolePermission.findFirst.mockResolvedValue(null);

    const { canDo } = await import("../authz/decision");
    const result = await canDo(userId, "read", "folder", folderId, orgId);

    expect(result).toEqual({ allowed: false, reason: "no RBAC ceiling" });
  });

  test("the circuit breaker opens after repeated failures and then bypasses Redis entirely", async () => {
    const { setRedisClientForTests } = await import("../cache/redisClient");
    const redis = brokenRedisClient();
    setRedisClientForTests(redis);
    const { withFailOpen, resetCircuitBreakerForTests } = await import("../cache/failOpen");
    resetCircuitBreakerForTests();

    const op = vi.fn(async (r: any) => {
      await r.get("probe");
      return "ok";
    });

    // Drive the breaker past its failure threshold.
    for (let i = 0; i < 5; i++) {
      await withFailOpen(op, "fallback");
    }
    expect(op).toHaveBeenCalledTimes(5);

    // Once open, withFailOpen must bypass without touching Redis at all —
    // bounding latency even while the outage persists.
    op.mockClear();
    const started = Date.now();
    const result = await withFailOpen(op, "fallback");
    const elapsedMs = Date.now() - started;

    expect(result).toBe("fallback");
    expect(op).not.toHaveBeenCalled();
    expect(elapsedMs).toBeLessThan(50);
  });
});
