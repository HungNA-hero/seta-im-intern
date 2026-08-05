export interface RequestContext {
  userId: string | null;
  currentOrgId: string | null;
  isMember: boolean;
  roles: string[];
  olpEnabled: boolean;
}

export interface RequestContextUser {
  isActive: boolean;
  isMember: boolean;
  roles: Array<{ id: string; code: string }>;
}

export interface RequestContextDataSource {
  findUser(userId: string, orgId: string | null): Promise<RequestContextUser | null>;
  getOlpEnabled(orgId: string): Promise<boolean>;
}

export interface RequestAuthorizationSnapshotWriter {
  set(snapshot: {
    userId: string;
    orgId: string;
    roleCodes: string[];
    roleIds: string[];
    olpEnabled: boolean;
    factMemo: Map<string, Promise<unknown>>;
  }): void;
}

export interface RequestContextLoader {
  load(userId: string | null, orgId: string | null): Promise<RequestContext>;
}

function emptyContext(): RequestContext {
  return {
    userId: null,
    currentOrgId: null,
    isMember: false,
    roles: [],
    olpEnabled: false,
  };
}

export function createRequestContextLoader(
  dataSource: RequestContextDataSource,
  snapshotWriter: RequestAuthorizationSnapshotWriter,
): RequestContextLoader {
  return {
    async load(userId, orgId): Promise<RequestContext> {
      if (!userId) return emptyContext();

      if (!orgId) {
        const user = await dataSource.findUser(userId, null);
        if (!user?.isActive) return emptyContext();
        return {
          userId,
          currentOrgId: null,
          isMember: false,
          roles: [],
          olpEnabled: false,
        };
      }

      const [user, olpEnabled] = await Promise.all([
        dataSource.findUser(userId, orgId),
        dataSource.getOlpEnabled(orgId),
      ]);
      if (!user?.isActive) return emptyContext();

      const roleCodes = user.roles.map((role) => role.code);
      snapshotWriter.set({
        userId,
        orgId,
        roleCodes,
        roleIds: user.roles.map((role) => role.id),
        olpEnabled,
        factMemo: new Map(),
      });
      return {
        userId,
        currentOrgId: orgId,
        isMember: user.isMember,
        roles: roleCodes,
        olpEnabled,
      };
    },
  };
}
