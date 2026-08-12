import { beforeEach, describe, expect, test, vi } from "vitest";
import { createAuthorizationService } from "../authz/authorizationService";
import { AuthorizationServiceDependencies } from "../authz/contracts";

function dependencies(): AuthorizationServiceDependencies {
  return {
    repository: {
      findUser: vi.fn().mockResolvedValue({
        isActive: true,
        roles: [{ id: "role-1", code: "member", orgId: "org-1" }],
      }),
      getOlpEnabled: vi.fn().mockResolvedValue(false),
      listPermissionActions: vi.fn().mockResolvedValue([{ code: "read", id: "action-read" }]),
      hasRbacCeiling: vi.fn().mockResolvedValue(true),
      findGrantedResourceIds: vi.fn().mockResolvedValue([]),
    },
    epochs: {
      getAssetEpoch: vi.fn().mockResolvedValue(0),
      getUserEpoch: vi.fn().mockResolvedValue(0),
      getRoleEpochs: vi.fn().mockResolvedValue([0]),
    },
    decisions: {
      read: vi.fn().mockResolvedValue(undefined),
      write: vi.fn().mockResolvedValue(undefined),
    },
    hierarchy: {
      getFolderPath: vi.fn().mockResolvedValue(null),
      getMetadataFolderId: vi.fn().mockResolvedValue(null),
    },
    trainerAdmin: { evaluate: vi.fn().mockReturnValue(null) },
    getRequestSnapshot: vi.fn().mockReturnValue(undefined),
    runSingleFlight: (_key, work) => work(),
  };
}

describe("createAuthorizationService", () => {
  let deps: AuthorizationServiceDependencies;

  beforeEach(() => {
    deps = dependencies();
  });

  test("evaluates RBAC through injected ports", async () => {
    const service = createAuthorizationService(deps);

    await expect(
      service.canDo({
        userId: "user-1",
        orgId: "org-1",
        action: "read",
        resourceType: "folder",
        resourceId: "folder-1",
      }),
    ).resolves.toEqual({ allowed: true, reason: null });

    expect(deps.repository.hasRbacCeiling).toHaveBeenCalledWith({
      roleIds: ["role-1"],
      permissionActionIds: ["action-read"],
      resourceType: "folder",
    });
    expect(deps.decisions.write).toHaveBeenCalledTimes(1);
  });

  test("returns a cached decision without evaluating grants", async () => {
    vi.mocked(deps.decisions.read).mockResolvedValue({
      allowed: false,
      reason: "cached deny",
    });
    const service = createAuthorizationService(deps);

    await expect(
      service.canDo({
        userId: "user-1",
        orgId: "org-1",
        action: "read",
        resourceType: "folder",
        resourceId: "folder-1",
      }),
    ).resolves.toEqual({ allowed: false, reason: "cached deny" });

    expect(deps.repository.hasRbacCeiling).not.toHaveBeenCalled();
    expect(deps.decisions.write).not.toHaveBeenCalled();
  });

  test("denies missing organization context without touching dependencies", async () => {
    const service = createAuthorizationService(deps);

    await expect(
      service.canDo({
        userId: "user-1",
        orgId: null,
        action: "read",
        resourceType: "folder",
        resourceId: "folder-1",
      }),
    ).resolves.toEqual({ allowed: false, reason: "no org context" });
    expect(deps.repository.findUser).not.toHaveBeenCalled();
  });
});
