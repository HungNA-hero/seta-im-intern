export type FolderDeletionStatus =
  | "queued"
  | "running"
  | "succeeded"
  | "failed"
  | "cancelled";

export interface GoFolderDeletionPreview {
  id: string;
  root_folder_id: string;
  active_folder_count: number;
  active_metadata_count: number;
  tombstone_folder_count: number;
  tombstone_metadata_count: number;
  total_rows: number;
  confirmation_token: string;
  expires_at: string;
}

export interface GoFolderDeletionJob {
  id: string;
  org_id: string;
  root_folder_id: string;
  requested_by: string;
  status: FolderDeletionStatus;
  active_folder_count: number;
  active_metadata_count: number;
  tombstone_folder_count: number;
  tombstone_metadata_count: number;
  deleted_folder_count: number;
  deleted_metadata_count: number;
  attempts: number;
  manual_retries: number;
  last_error_code?: string | null;
  queued_at?: string | null;
  started_at?: string | null;
  completed_at?: string | null;
  cancelled_at?: string | null;
}

export function toFolderDeletionPreview(preview: GoFolderDeletionPreview) {
  return {
    id: preview.id,
    rootFolderId: preview.root_folder_id,
    activeFolderCount: preview.active_folder_count,
    activeMetadataCount: preview.active_metadata_count,
    tombstoneFolderCount: preview.tombstone_folder_count,
    tombstoneMetadataCount: preview.tombstone_metadata_count,
    totalRows: preview.total_rows,
    confirmationToken: preview.confirmation_token,
    expiresAt: preview.expires_at,
  };
}

export function toFolderDeletionJob(job: GoFolderDeletionJob) {
  return {
    id: job.id,
    orgId: job.org_id,
    rootFolderId: job.root_folder_id,
    requestedBy: job.requested_by,
    status: job.status,
    activeFolderCount: job.active_folder_count,
    activeMetadataCount: job.active_metadata_count,
    tombstoneFolderCount: job.tombstone_folder_count,
    tombstoneMetadataCount: job.tombstone_metadata_count,
    deletedFolderCount: job.deleted_folder_count,
    deletedMetadataCount: job.deleted_metadata_count,
    attempts: job.attempts,
    manualRetries: job.manual_retries,
    lastErrorCode: job.last_error_code ?? null,
    queuedAt: job.queued_at ?? null,
    startedAt: job.started_at ?? null,
    completedAt: job.completed_at ?? null,
    cancelledAt: job.cancelled_at ?? null,
  };
}
