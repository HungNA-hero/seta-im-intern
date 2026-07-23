import { describe, test, expect, vi, beforeEach } from "vitest";
import { randomUUID } from "node:crypto";

const { mockPrisma } = vi.hoisted(() => ({
  mockPrisma: {
    user: { findUnique: vi.fn() },
    organization: { findUnique: vi.fn() },
  },
}));

vi.mock("../db/prisma", () => ({ prisma: mockPrisma }));

describe("olpEnabled stays an uncached per-request read", () => {
  const orgId = randomUUID();
  const userId = randomUUID();

  beforeEach(() => {
    vi.clearAllMocks();
    mockPrisma.user.findUnique.mockResolvedValue({
      id: userId,
      isActive: true,
      orgMembers: [{ orgId }],
      userRoles: [],
    });
  });

  test("a toggle takes effect on the very next request with no invalidation signal needed", async () => {
    const { loadRequestContext } = await import("../graphql/context");

    mockPrisma.organization.findUnique.mockResolvedValueOnce({ olpEnabled: false });
    const before = await loadRequestContext(userId, orgId);
    expect(before.olpEnabled).toBe(false);

    mockPrisma.organization.findUnique.mockResolvedValueOnce({ olpEnabled: true });
    const after = await loadRequestContext(userId, orgId);
    expect(after.olpEnabled).toBe(true);

    expect(mockPrisma.organization.findUnique).toHaveBeenCalledTimes(2);
  });
});
