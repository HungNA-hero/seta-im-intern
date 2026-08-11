import { AsyncLocalStorage } from "node:async_hooks";
import { AuthorizationRequestFacts, AuthorizationRequestSnapshot } from "./contracts";

const storage = new AsyncLocalStorage<AuthorizationRequestSnapshot>();

export function beginAuthorizationRequest(facts: AuthorizationRequestFacts): void {
  storage.enterWith({ ...facts, factMemo: new Map() });
}

export function getAuthzRequestContext(): AuthorizationRequestSnapshot | undefined {
  return storage.getStore();
}
