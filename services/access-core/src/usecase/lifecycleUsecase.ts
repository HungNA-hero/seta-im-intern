import { assetPath, LIFECYCLE_JOBS_PATH, LIFECYCLE_RESTORE_PATH, unwrapEnvelope } from "../clients/assetClient";
import { GoLifecycleJob, LifecycleJob, toLifecycleJob } from "../domain/lifecycleJob";
import { invalidAssetFact } from "../clients/assetErrors";
import { GraphQLContext } from "../graphql/context";
import { authorizedFetch } from "./authorizedAssetFetch";
import { authorizeLifecycleJob, authorizeLifecycleRestore } from "./restoreAuthorization";

function decodeLifecycleJob(raw: any): GoLifecycleJob {
  const validOperation = raw?.operation === "DELETE" || raw?.operation === "RESTORE" || raw?.operation === "PURGE";
  const validStatus =
    raw?.status === "QUEUED" ||
    raw?.status === "RUNNING" ||
    raw?.status === "SUCCEEDED" ||
    raw?.status === "FAILED" ||
    raw?.status === "SUPPRESSED";
  const validRootType = raw?.root_resource_type === "FOLDER" || raw?.root_resource_type === "METADATA";
  if (
    typeof raw?.id !== "string" ||
    !(raw?.unit_id === null || typeof raw?.unit_id === "string") ||
    !validRootType ||
    typeof raw?.root_resource_id !== "string" ||
    typeof raw?.root_folder_id !== "string" ||
    typeof raw?.root_folder_path !== "string" ||
    typeof raw?.requested_by !== "string" ||
    !validOperation ||
    !validStatus ||
    typeof raw?.attempts !== "number"
  ) {
    return invalidAssetFact();
  }
  return raw as GoLifecycleJob;
}

export async function restoreLifecycleUnit(ctx: GraphQLContext, orgId: string, unitId: string): Promise<LifecycleJob> {
  await authorizeLifecycleRestore(ctx, orgId, unitId);
  const response = await authorizedFetch({
    ctx,
    orgId,
    path: assetPath(LIFECYCLE_RESTORE_PATH, { orgId, unitId }),
    init: { method: "POST" },
  });
  const job = await unwrapEnvelope(response, "job", decodeLifecycleJob, "Failed to queue lifecycle restore");
  return toLifecycleJob(job);
}

export async function getLifecycleJob(ctx: GraphQLContext, orgId: string, jobId: string): Promise<LifecycleJob> {
  // This is a trusted internal fetch only. We deliberately authorize after
  // decoding the root identity rather than trusting client-provided jobId.
  const response = await authorizedFetch({
    ctx,
    orgId,
    path: assetPath(LIFECYCLE_JOBS_PATH, { orgId, id: jobId }),
    init: { method: "GET" },
  });
  const job = await unwrapEnvelope(response, "job", decodeLifecycleJob, "Failed to load lifecycle job");
  await authorizeLifecycleJob(ctx, orgId, job);
  return toLifecycleJob(job);
}
