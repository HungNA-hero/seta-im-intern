import { getAssetEpoch, getRoleEpochs, getUserEpoch } from "../cache/epoch";
import { readDecision, writeDecision } from "../cache/decisionCache";
import { singleFlight } from "../cache/singleFlight";
import { getFolderMeta, getMetadataMeta } from "../clients/assetClient";
import {
  findAuthorizationUser,
  findGrantedResourceIds,
  hasRbacCeiling,
  isOlpEnabled,
  listPermissionActions,
} from "../db/queries/authorization";
import { createAuthorizationService } from "./authorizationService";
import { getAuthzRequestContext } from "./authzRequestContext";
import { auditTrainerAdminDecision, getTrainerAdminGateState } from "./trainerAdmin";

export const authorizationService = createAuthorizationService({
  repository: {
    findUser: findAuthorizationUser,
    getOlpEnabled: isOlpEnabled,
    listPermissionActions,
    hasRbacCeiling,
    findGrantedResourceIds,
  },
  epochs: {
    getAssetEpoch,
    getUserEpoch,
    getRoleEpochs,
  },
  decisions: {
    read: readDecision,
    write: writeDecision,
  },
  hierarchy: {
    async getFolderPath(orgId, userId, folderId) {
      return (await getFolderMeta(orgId, userId, folderId))?.path ?? null;
    },
    async getMetadataFolderId(orgId, userId, metadataId) {
      return (await getMetadataMeta(orgId, userId, metadataId))?.folderId ?? null;
    },
  },
  trainerAdmin: {
    evaluate(userId, roleCodes) {
      if (process.env.NODE_ENV === "production" || !roleCodes.includes("trainer_admin")) {
        return null;
      }
      const state = getTrainerAdminGateState();
      auditTrainerAdminDecision(userId, state.enabled, state.reason);
      return state.enabled ? { allowed: true, reason: "trainer_admin" } : null;
    },
  },
  getRequestSnapshot: getAuthzRequestContext,
  runSingleFlight: singleFlight,
});

export function resetInProcessAuthzCachesForTests(): void {
  authorizationService.resetInProcessCaches();
}
