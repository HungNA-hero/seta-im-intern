import { describe, test, expect, vi, beforeEach } from "vitest";
import { randomUUID } from "node:crypto";

const { mockPrisma, decisionCacheMock, epochMock } = vi.hoisted(() => ({
  mockPrisma: {
    user: { findUnique: vi.fn() },
    organization: { findUnique: vi.fn() },
    permissionAction: { findMany: vi.fn() },
    rolePermission: { findFirst: vi.fn() },
    objectPermission: { findMany: vi.fn() },
  },
  decisionCacheMock: {
    readDecision: vi.fn().mockResolvedValue(undefined),
    writeDecision: vi.fn().mockResolvedValue(undefined),
  },
  epochMock: {
    getAssetEpoch: vi.fn().mockResolvedValue(0),
    getUserEpoch: vi.fn().mockResolvedValue(0),
    getRoleEpochs: vi.fn().mockResolvedValue([0]),
  },
}));

vi.mock("../db/prisma", () => ({ prisma: mockPrisma }));
vi.mock("../cache/decisionCache", () => decisionCacheMock);
vi.mock("../cache/epoch", () => epochMock);
vi.mock("../clients/assetClient", () => ({
  getFolderMeta: vi.fn().mockRejectedValue(new Error("asset-core 503")),
  getMetadataMeta: vi.fn().mockResolvedValue(null),
}));

describe("a transient asset-core failure is never written to the decision cache", () => {
  const orgId = randomUUID();
  const userId = randomUUID();
  const folderId = randomUUID();
  const roleId = randomUUID();

  beforeEach(() => {
    vi.clearAllMocks();
    decisionCacheMock.readDecision.mockResolvedValue(undefined);
    epochMock.getAssetEpoch.mockResolvedValue(0);
    epochMock.getUserEpoch.mockResolvedValue(0);
    epochMock.getRoleEpochs.mockResolvedValue([0]);
    mockPrisma.permissionAction.findMany.mockResolvedValue([
      { code: "read", id: "action-read" },
    ]);
    mockPrisma.user.findUnique.mockResolvedValue({
      id: userId,
      isActive: true,
      userRoles: [{ roleId, orgId, role: { code: "member" } }],
    });
    mockPrisma.organization.findUnique.mockResolvedValue({ olpEnabled: true });
    // No direct grant on the folder — inheritance must be checked, which is
    // where the failing asset-core call is reached.
    mockPrisma.objectPermission.findMany.mockResolvedValue([]);
  });

  test("canDo propagates the dependency failure instead of caching a deny", async () => {
    const { canDo } = await import("../authz/decision");

    await expect(canDo(userId, "read", "folder", folderId, orgId)).rejects.toThrow(
      "asset-core 503",
    );
    expect(decisionCacheMock.writeDecision).not.toHaveBeenCalled();
  });
});
