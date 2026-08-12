import { beforeEach, describe, expect, test, vi } from "vitest";
import { createAuthorizationService } from "../authz/authorizationService";
import { AuthorizationRequestSnapshot, AuthorizationServiceDependencies } from "../authz/contracts";

const USER_ID = "user-1";
const ORG_ID = "org-1";
const OTHER_ORG_ID = "org-2";

function dependencies(): AuthorizationServiceDependencies {
  return {
    repository: {
      findUser: vi.fn().mockResolvedValue({
        isActive: true,
        roles: [
          { id: "role-1", code: "member", orgId: ORG_ID },
          { id: "role-admin", code: "trainer_admin", orgId: OTHER_ORG_ID },
        ],
      }),
      getOlpEnabled: vi.fn().mockResolvedValue(true),
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
      getFolderPath: vi.fn().mockResolvedValue("root.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
      getMetadataFolderId: vi.fn().mockResolvedValue(null),
    },
    trainerAdmin: {
      evaluate: vi.fn((_userId: string, roleCodes: string[]) =>
        roleCodes.includes("trainer_admin") ? { allowed: true, reason: "trainer_admin" } : null,
      ),
    },
    getRequestSnapshot: vi.fn().mockReturnValue(undefined),
    runSingleFlight: (_key, work) => work(),
  };
}

function snapshot(overrides: Partial<AuthorizationRequestSnapshot> = {}): AuthorizationRequestSnapshot {
  return {
    userId: USER_ID,
    orgId: ORG_ID,
    globalRoleCodes: ["member", "trainer_admin"],
    orgRoleCodes: ["member"],
    roleIds: ["role-1"],
    olpEnabled: false,
    factMemo: new Map(),
    ...overrides,
  };
}

function readFolder(userId = USER_ID, orgId: string | null = ORG_ID) {
  return { userId, orgId, action: "read", resourceType: "folder", resourceId: "folder-1" } as const;
}

describe("authorization request snapshot", () => {
  let deps: AuthorizationServiceDependencies;

  beforeEach(() => {
    deps = dependencies();
  });

  test("resolves roles from the snapshot without querying the repository", async () => {
    deps.getRequestSnapshot = vi.fn().mockReturnValue(snapshot());
    const service = createAuthorizationService(deps);

    await service.canDo(readFolder());

    expect(deps.repository.findUser).not.toHaveBeenCalled();
  });

  test("honours a trainer_admin role held in another organization, as the repository path does", async () => {
    const fromSnapshot = createAuthorizationService({
      ...deps,
      getRequestSnapshot: vi.fn().mockReturnValue(snapshot()),
    });
    const fromRepository = createAuthorizationService(dependencies());

    await expect(fromSnapshot.canDo(readFolder())).resolves.toEqual({ allowed: true, reason: "trainer_admin" });
    await expect(fromRepository.canDo(readFolder())).resolves.toEqual({ allowed: true, reason: "trainer_admin" });
  });

  test("does not treat an org_admin role from another organization as org admin", async () => {
    deps.trainerAdmin = { evaluate: vi.fn().mockReturnValue(null) };
    deps.getRequestSnapshot = vi.fn().mockReturnValue(
      snapshot({
        globalRoleCodes: ["member", "org_admin"],
        orgRoleCodes: ["member"],
      }),
    );
    const service = createAuthorizationService(deps);

    await expect(service.canDo(readFolder())).resolves.not.toEqual({ allowed: true, reason: "org_admin" });
  });

  test("memoizes a folder lookup across two decisions in the same request", async () => {
    deps.trainerAdmin = { evaluate: vi.fn().mockReturnValue(null) };
    deps.getRequestSnapshot = vi.fn().mockReturnValue(snapshot({ olpEnabled: true }));
    const service = createAuthorizationService(deps);

    await service.canDo(readFolder());
    await service.canDo(readFolder());

    expect(deps.hierarchy.getFolderPath).toHaveBeenCalledTimes(1);
  });

  test("does not reuse a memo belonging to a different user", async () => {
    deps.trainerAdmin = { evaluate: vi.fn().mockReturnValue(null) };
    deps.getRequestSnapshot = vi.fn().mockReturnValue(snapshot({ olpEnabled: true }));
    const service = createAuthorizationService(deps);

    await service.canDo(readFolder());
    await service.canDo(readFolder("user-2"));

    expect(deps.hierarchy.getFolderPath).toHaveBeenCalledTimes(2);
    expect(deps.repository.findUser).toHaveBeenCalledWith("user-2");
  });
});
