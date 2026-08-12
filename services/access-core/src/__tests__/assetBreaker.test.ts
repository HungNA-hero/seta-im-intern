import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
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
vi.mock("../cache/factCache", () => ({
  readFolderFactThrough: (_orgId: string, _id: string, loader: () => Promise<unknown>) => loader(),
  readItemFactThrough: (_orgId: string, _id: string, loader: () => Promise<unknown>) => loader(),
}));

import { createAssetBreakerForTests, fireAssetRequest, resetAssetBreakerForTests } from "../clients/assetBreaker";
import { canDo } from "../authz/decision";
import { resetInProcessAuthzCachesForTests } from "../authz/authorizationRoot";
import { beginAuthorizationRequest } from "../authz/authzRequestContext";
import { createMetadata } from "../usecase/metadataUsecase";
import { makeBreakerOptions } from "./helpers/assetBreakerTestFixtures";

const originalFetch = global.fetch;
const mockFetch = vi.fn();

const testOptions = makeBreakerOptions({
  volumeThreshold: 4,
  resetTimeoutMs: 5000,
  capacity: 20,
});

beforeEach(() => {
  global.fetch = mockFetch;
  mockFetch.mockReset();
  vi.clearAllMocks();
  resetAssetBreakerForTests();
  resetInProcessAuthzCachesForTests();
  decisionCacheMock.readDecision.mockResolvedValue(undefined);
  decisionCacheMock.writeDecision.mockResolvedValue(undefined);
  epochMock.getAssetEpoch.mockResolvedValue(0);
  epochMock.getUserEpoch.mockResolvedValue(0);
  epochMock.getRoleEpochs.mockResolvedValue([0]);
});

afterEach(() => {
  global.fetch = originalFetch;
  vi.useRealTimers();
});

describe("asset dependency breaker", () => {
  it("opens after the configured failure volume and then rejects without I/O", async () => {
    const harness = createAssetBreakerForTests(testOptions);
    mockFetch.mockResolvedValue(new Response(null, { status: 503 }));

    try {
      for (let index = 0; index < testOptions.volumeThreshold; index += 1) {
        await harness.fire("http://asset/test", {});
      }
      expect(harness.snapshot().state).toBe("open");

      mockFetch.mockClear();
      const startedAt = performance.now();
      await expect(harness.fire("http://asset/test", {})).rejects.toMatchObject({
        extensions: { code: "INTERNAL_ERROR", service: "access-core" },
      });

      expect(performance.now() - startedAt).toBeLessThan(50);
      expect(mockFetch).not.toHaveBeenCalled();
    } finally {
      harness.shutdown();
    }
  });

  it("does not count sustained 404 responses as failures", async () => {
    const harness = createAssetBreakerForTests(testOptions);
    mockFetch.mockResolvedValue(new Response(null, { status: 404 }));

    try {
      for (let index = 0; index < 12; index += 1) {
        await harness.fire("http://asset/missing", {});
      }
      expect(harness.snapshot()).toMatchObject({
        state: "closed",
        stats: { failures: 0 },
      });
    } finally {
      harness.shutdown();
    }
  });

  it("stays closed below volumeThreshold", async () => {
    const harness = createAssetBreakerForTests(testOptions);
    mockFetch.mockResolvedValue(new Response(null, { status: 500 }));

    try {
      for (let index = 0; index < testOptions.volumeThreshold - 1; index += 1) {
        await harness.fire("http://asset/test", {});
      }
      expect(harness.snapshot().state).toBe("closed");
    } finally {
      harness.shutdown();
    }
  });

  it("counts one 5xx as one attempt and one breaker failure", async () => {
    const harness = createAssetBreakerForTests({
      ...testOptions,
      volumeThreshold: 10,
    });
    mockFetch.mockResolvedValue(new Response(null, { status: 500 }));

    try {
      await expect(harness.fire("http://asset/test", {})).resolves.toMatchObject({
        status: 500,
      });
      expect(mockFetch).toHaveBeenCalledTimes(1);
      expect(harness.snapshot().stats.failures).toBe(1);
    } finally {
      harness.shutdown();
    }
  });

  it("does not retry a transport rejection", async () => {
    const harness = createAssetBreakerForTests(testOptions);
    mockFetch.mockRejectedValue(new Error("ECONNREFUSED"));

    try {
      await expect(harness.fire("http://asset/test", {})).rejects.toThrow("ECONNREFUSED");
      expect(mockFetch).toHaveBeenCalledTimes(1);
    } finally {
      harness.shutdown();
    }
  });

  it("does not retry a deadline abort", async () => {
    vi.useFakeTimers();
    const harness = createAssetBreakerForTests(testOptions);
    mockFetch.mockImplementation(
      (_url: string, init: RequestInit) =>
        new Promise((_resolve, reject) => {
          init.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
        }),
    );

    try {
      const request = harness.fire("http://asset/test", {});
      const rejection = expect(request).rejects.toMatchObject({
        name: "AbortError",
      });
      await vi.advanceTimersByTimeAsync(3000);
      await rejection;
      expect(mockFetch).toHaveBeenCalledTimes(1);
    } finally {
      harness.shutdown();
    }
  });

  it("shares one service-wide breaker with proxied metadata mutations", async () => {
    mockFetch.mockResolvedValue(new Response(null, { status: 503 }));
    for (let index = 0; index < 10; index += 1) {
      await fireAssetRequest("http://asset/folders", {});
    }

    beginAuthorizationRequest({
      userId: "user-1",
      orgId: "org-1",
      globalRoleCodes: ["org_admin"],
      orgRoleCodes: ["org_admin"],
      roleIds: ["role-admin"],
      olpEnabled: true,
    });
    mockFetch.mockClear();

    await expect(
      createMetadata(
        {
          userId: "user-1",
          currentOrgId: "org-1",
          isMember: true,
          roles: ["org_admin"],
          olpEnabled: true,
        },
        "org-1",
        { folderId: "folder-1", title: "Blocked write" },
      ),
    ).rejects.toMatchObject({
      extensions: { code: "INTERNAL_ERROR", service: "access-core" },
    });
    expect(mockFetch).not.toHaveBeenCalled();
  });

  it("fails closed for a cold ancestor-dependent canDo decision", async () => {
    mockFetch.mockResolvedValue(new Response(null, { status: 503 }));
    for (let index = 0; index < 10; index += 1) {
      await fireAssetRequest("http://asset/folders", {});
    }

    const orgId = randomUUID();
    const userId = randomUUID();
    const folderId = randomUUID();
    const roleId = randomUUID();
    mockPrisma.permissionAction.findMany.mockResolvedValue([{ code: "read", id: "action-read" }]);
    mockPrisma.user.findUnique.mockResolvedValue({
      id: userId,
      isActive: true,
      userRoles: [{ roleId, orgId, role: { code: "member" } }],
    });
    mockPrisma.organization.findUnique.mockResolvedValue({ olpEnabled: true });
    mockPrisma.rolePermission.findFirst.mockResolvedValue({ id: "ceiling" });
    mockPrisma.objectPermission.findMany.mockResolvedValue([]);

    const result = canDo({
      userId: userId,
      action: "read",
      resourceType: "folder",
      resourceId: folderId,
      orgId: orgId,
    });

    await expect(result).rejects.toMatchObject({
      extensions: { code: "INTERNAL_ERROR", service: "access-core" },
    });
    await expect(result.then((decision) => decision.allowed)).rejects.toBeDefined();
    expect(decisionCacheMock.readDecision).toHaveBeenCalled();
    expect(decisionCacheMock.writeDecision).not.toHaveBeenCalled();
  });
});
