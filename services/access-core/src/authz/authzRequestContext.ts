import { AsyncLocalStorage } from "node:async_hooks";

export interface AuthzRequestContext {
  userId: string;
  orgId: string;
  roleCodes: string[];
  roleIds: string[];
  olpEnabled: boolean;
  factMemo: Map<string, Promise<unknown>>;
}

const storage = new AsyncLocalStorage<AuthzRequestContext>();

export function setAuthzRequestContext(ctx: AuthzRequestContext): void {
  storage.enterWith(ctx);
}

export function getAuthzRequestContext(): AuthzRequestContext | undefined {
  return storage.getStore();
}
