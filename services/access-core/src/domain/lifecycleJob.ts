export type LifecycleJobOperation = "DELETE" | "RESTORE" | "PURGE";
export type LifecycleJobStatus = "QUEUED" | "RUNNING" | "SUCCEEDED" | "FAILED" | "SUPPRESSED";
export type LifecycleRootResourceType = "FOLDER" | "METADATA";

// GoLifecycleJob is the trusted internal Asset Core payload. Root fields are
// intentionally retained here only until Access Core has completed its own
// authorization check; the public GraphQL type omits them.
export interface GoLifecycleJob {
  id: string;
  org_id: string;
  unit_id: string | null;
  root_resource_type: LifecycleRootResourceType;
  root_resource_id: string;
  root_folder_id: string;
  root_folder_path: string;
  requested_by: string;
  operation: LifecycleJobOperation;
  status: LifecycleJobStatus;
  attempts: number;
  failure_code?: string | null;
  queued_at?: string | null;
  started_at?: string | null;
  completed_at?: string | null;
}

export interface LifecycleJob {
  jobId: string;
  lifecycleUnitId: string | null;
  operation: LifecycleJobOperation;
  status: LifecycleJobStatus;
  attempts: number;
  failureCode: string | null;
  queuedAt: string | null;
  startedAt: string | null;
  completedAt: string | null;
}

export function toLifecycleJob(job: GoLifecycleJob): LifecycleJob {
  return {
    jobId: job.id,
    lifecycleUnitId: job.unit_id ?? null,
    operation: job.operation,
    status: job.status,
    attempts: job.attempts,
    failureCode: job.failure_code ?? null,
    queuedAt: job.queued_at ?? null,
    startedAt: job.started_at ?? null,
    completedAt: job.completed_at ?? null,
  };
}
