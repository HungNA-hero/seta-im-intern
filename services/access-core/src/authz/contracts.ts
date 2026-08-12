import { PermissionActionCode, ResourceType } from "@prisma/client";

export interface AuthorizationDecision {
  allowed: boolean;
  reason: string | null;
}

export interface AuthorizationRole {
  id: string;
  code: string;
  orgId: string;
}

export interface AuthorizationUser {
  isActive: boolean;
  roles: AuthorizationRole[];
}

export interface GrantedResourceQuery {
  userId: string;
  orgId: string;
  resourceType: ResourceType;
  resourceIds: string[];
  permissionActionIds: string[];
  roleIds: string[];
}

export interface RbacCeilingQuery {
  roleIds: string[];
  permissionActionIds: string[];
  resourceType: ResourceType;
}

export interface AuthorizationRepository {
  findUser(userId: string): Promise<AuthorizationUser | null>;
  getOlpEnabled(orgId: string): Promise<boolean>;
  listPermissionActions(): Promise<Array<{ code: string; id: string }>>;
  hasRbacCeiling(query: RbacCeilingQuery): Promise<boolean>;
  findGrantedResourceIds(query: GrantedResourceQuery): Promise<string[]>;
}

export interface AuthorizationEpochReader {
  getAssetEpoch(orgId: string): Promise<number>;
  getUserEpoch(orgId: string, userId: string): Promise<number>;
  getRoleEpochs(orgId: string, roleIds: string[]): Promise<number[]>;
}

export interface AuthorizationDecisionStore {
  read(key: string): Promise<AuthorizationDecision | undefined>;
  write(key: string, decision: AuthorizationDecision): Promise<void>;
}

export interface AssetHierarchyReader {
  getFolderPath(orgId: string, userId: string, folderId: string): Promise<string | null>;
  getMetadataFolderId(orgId: string, userId: string, metadataId: string): Promise<string | null>;
}

export interface AuthorizationRequestFacts {
  userId: string;
  orgId: string;
  globalRoleCodes: string[];
  orgRoleCodes: string[];
  roleIds: string[];
  olpEnabled: boolean;
}

export interface AuthorizationRequestSnapshot extends AuthorizationRequestFacts {
  factMemo: Map<string, Promise<unknown>>;
}

export interface TrainerAdminPolicy {
  evaluate(userId: string, roleCodes: string[]): AuthorizationDecision | null;
}

export interface AuthorizationServiceDependencies {
  repository: AuthorizationRepository;
  epochs: AuthorizationEpochReader;
  decisions: AuthorizationDecisionStore;
  hierarchy: AssetHierarchyReader;
  trainerAdmin: TrainerAdminPolicy;
  getRequestSnapshot(): AuthorizationRequestSnapshot | undefined;
  runSingleFlight<T>(key: string, work: () => Promise<T>): Promise<T>;
}

export interface CanDoRequest {
  userId: string;
  action: PermissionActionCode;
  resourceType: ResourceType;
  resourceId: string;
  orgId: string | null;
}

export interface KnownAncestorCanDoRequest extends CanDoRequest {
  ancestorIds: string[];
}

export interface ResourceHierarchy {
  ancestorIds?: string[];
}

export interface FilterAllowedResourcesRequest<T extends { id: string } = { id: string }> {
  userId: string;
  orgId: string;
  action: PermissionActionCode;
  resourceType: ResourceType;
  resourceIds: string[];
  items?: T[];
  getHierarchy?: (item: T) => ResourceHierarchy;
}

export interface FilterVisibleResourcesRequest<T extends { id: string }> {
  userId: string;
  orgId: string;
  action: PermissionActionCode;
  resourceType: ResourceType;
  items: T[];
  getHierarchy?: (item: T) => ResourceHierarchy;
}

export interface AuthorizationService {
  canDo(request: CanDoRequest): Promise<AuthorizationDecision>;
  canDoWithKnownAncestors(request: KnownAncestorCanDoRequest): Promise<AuthorizationDecision>;
  filterAllowedResourceIds<T extends { id: string }>(request: FilterAllowedResourcesRequest<T>): Promise<Set<string>>;
  filterVisible<T extends { id: string }>(request: FilterVisibleResourcesRequest<T>): Promise<T[]>;
  resetInProcessCaches(): void;
}
