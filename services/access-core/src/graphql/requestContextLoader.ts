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
  roles: Array<{ id: string; code: string; orgId: string }>;
}

export interface RequestContextDataSource {
  findUser(userId: string, orgId: string | null): Promise<RequestContextUser | null>;
  getOlpEnabled(orgId: string): Promise<boolean>;
}

export interface AuthorizationRequestBeginner {
  begin(facts: {
    userId: string;
    orgId: string;
    globalRoleCodes: string[];
    orgRoleCodes: string[];
    roleIds: string[];
    olpEnabled: boolean;
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
  authorizationRequest: AuthorizationRequestBeginner,
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

      const orgRoles = user.roles.filter((role) => role.orgId === orgId);
      const orgRoleCodes = orgRoles.map((role) => role.code);
      authorizationRequest.begin({
        userId,
        orgId,
        globalRoleCodes: user.roles.map((role) => role.code),
        orgRoleCodes,
        roleIds: orgRoles.map((role) => role.id),
        olpEnabled,
      });
      return {
        userId,
        currentOrgId: orgId,
        isMember: user.isMember,
        roles: orgRoleCodes,
        olpEnabled,
      };
    },
  };
}
