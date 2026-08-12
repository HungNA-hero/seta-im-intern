import { authorizationService } from "./authorizationRoot";
import {
  AuthorizationDecision,
  CanDoRequest,
  FilterVisibleResourcesRequest,
  KnownAncestorCanDoRequest,
} from "./contracts";

export type FilterVisibleInput<T extends { id: string }> = FilterVisibleResourcesRequest<T>;

export function canDo(request: CanDoRequest): Promise<AuthorizationDecision> {
  return authorizationService.canDo(request);
}

export function canDoWithKnownAncestors(request: KnownAncestorCanDoRequest): Promise<AuthorizationDecision> {
  return authorizationService.canDoWithKnownAncestors(request);
}

export function filterVisible<T extends { id: string }>(input: FilterVisibleInput<T>): Promise<T[]> {
  return authorizationService.filterVisible(input);
}
