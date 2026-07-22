-- KAN-64: durable asynchronous recursive hard-delete jobs. The root folder
-- intentionally has no foreign key because a successful job deletes it last.
CREATE TABLE folder_deletion_jobs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organization_ref(org_id) ON DELETE RESTRICT,
    root_folder_id uuid NOT NULL,
    root_path ltree NOT NULL,
    requested_by uuid NOT NULL REFERENCES user_ref(user_id) ON DELETE RESTRICT,
    status varchar(16) NOT NULL,
    confirmation_token_hash bytea,
    preview_expires_at timestamptz,
    active_folder_count bigint NOT NULL DEFAULT 0,
    active_metadata_count bigint NOT NULL DEFAULT 0,
    tombstone_folder_count bigint NOT NULL DEFAULT 0,
    tombstone_metadata_count bigint NOT NULL DEFAULT 0,
    deleted_folder_count bigint NOT NULL DEFAULT 0,
    deleted_metadata_count bigint NOT NULL DEFAULT 0,
    attempts integer NOT NULL DEFAULT 0,
    manual_retries integer NOT NULL DEFAULT 0,
    next_run_at timestamptz,
    lease_owner text,
    lease_expires_at timestamptz,
    last_error_code varchar(64),
    queued_at timestamptz,
    started_at timestamptz,
    completed_at timestamptz,
    cancelled_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_folder_deletion_job_status CHECK (
        status IN ('previewed', 'queued', 'running', 'succeeded', 'failed', 'cancelled')
    ),
    CONSTRAINT chk_folder_deletion_job_counts CHECK (
        active_folder_count >= 0 AND active_metadata_count >= 0
        AND tombstone_folder_count >= 0 AND tombstone_metadata_count >= 0
        AND deleted_folder_count >= 0 AND deleted_metadata_count >= 0
        AND attempts >= 0 AND manual_retries >= 0
    )
);

CREATE INDEX idx_folder_deletion_jobs_claim
    ON folder_deletion_jobs (status, next_run_at, queued_at, created_at);

CREATE INDEX idx_folder_deletion_jobs_expired_lease
    ON folder_deletion_jobs (status, lease_expires_at)
    WHERE status = 'running';

CREATE INDEX idx_folder_deletion_jobs_root_path
    ON folder_deletion_jobs USING GIST (root_path);

CREATE TRIGGER trg_folder_deletion_jobs_set_updated_at
BEFORE UPDATE ON folder_deletion_jobs
FOR EACH ROW
EXECUTE FUNCTION set_updated_at();
