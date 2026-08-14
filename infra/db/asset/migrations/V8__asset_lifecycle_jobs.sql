-- KAN-83: one durable lifecycle job drives a bounded DELETE, RESTORE or PURGE
-- operation. V5 remains temporarily for the compatibility adapter; this table
-- becomes the common job representation before the old worker is retired.

-- A lifecycle job must never attach a unit from another organization. The
-- existing primary key on id is not a legal target for that composite FK.
ALTER TABLE asset_lifecycle_units
    ADD CONSTRAINT uq_asset_lifecycle_units_id_org UNIQUE (id, org_id);

CREATE TABLE asset_lifecycle_jobs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organization_ref(org_id) ON DELETE RESTRICT,
    unit_id uuid NULL,
    legacy_folder_deletion_job_id uuid NULL REFERENCES folder_deletion_jobs(id) ON DELETE RESTRICT,
    root_resource_type varchar(16) NOT NULL,
    root_resource_id uuid NOT NULL,
    root_folder_id uuid NOT NULL,
    root_folder_path ltree NOT NULL,
    requested_by uuid NOT NULL REFERENCES user_ref(user_id) ON DELETE RESTRICT,
    operation varchar(16) NOT NULL,
    status varchar(16) NOT NULL,
    checkpoint jsonb NOT NULL DEFAULT '{}'::jsonb,
    attempts integer NOT NULL DEFAULT 0,
    next_run_at timestamptz,
    lease_owner text,
    lease_expires_at timestamptz,
    trace_id text,
    failure_code varchar(64),
    preview_token_hash bytea,
    preview_expires_at timestamptz,
    queued_at timestamptz,
    started_at timestamptz,
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT fk_asset_lifecycle_jobs_unit_org
        FOREIGN KEY (unit_id, org_id)
        REFERENCES asset_lifecycle_units (id, org_id)
        ON DELETE RESTRICT,
    CONSTRAINT chk_asset_lifecycle_jobs_root_type CHECK (
        root_resource_type IN ('FOLDER', 'METADATA')
    ),
    CONSTRAINT chk_asset_lifecycle_jobs_operation CHECK (
        operation IN ('DELETE', 'RESTORE', 'PURGE')
    ),
    CONSTRAINT chk_asset_lifecycle_jobs_status CHECK (
        status IN ('PREVIEWED', 'QUEUED', 'RUNNING', 'SUCCEEDED', 'FAILED', 'SUPPRESSED')
    ),
    CONSTRAINT chk_asset_lifecycle_jobs_checkpoint CHECK (
        jsonb_typeof(checkpoint) = 'object'
    ),
    CONSTRAINT chk_asset_lifecycle_jobs_attempts CHECK (attempts >= 0),
    CONSTRAINT chk_asset_lifecycle_jobs_preview_state CHECK (
        (status = 'PREVIEWED' AND operation = 'DELETE' AND unit_id IS NULL
            AND preview_token_hash IS NOT NULL AND preview_expires_at IS NOT NULL)
        OR (status <> 'PREVIEWED' AND unit_id IS NOT NULL)
    ),
    CONSTRAINT chk_asset_lifecycle_jobs_running_lease CHECK (
        (status = 'RUNNING' AND lease_owner IS NOT NULL AND lease_expires_at IS NOT NULL)
        OR (status <> 'RUNNING' AND lease_owner IS NULL AND lease_expires_at IS NULL)
    )
);

-- A unit can have one worker-owned operation at a time. Historical SUCCEEDED,
-- FAILED and SUPPRESSED jobs remain available for audit and retry decisions.
CREATE UNIQUE INDEX uq_asset_lifecycle_jobs_active_unit
    ON asset_lifecycle_jobs (unit_id)
    WHERE status IN ('QUEUED', 'RUNNING');

-- The V5 row remains only as the public compatibility projection while KAN-83
-- moves worker ownership to this table. One legacy job can map to one engine
-- job, which also allows a worker to adopt V5 jobs queued before deployment.
CREATE UNIQUE INDEX uq_asset_lifecycle_jobs_legacy_folder_deletion_job
    ON asset_lifecycle_jobs (legacy_folder_deletion_job_id)
    WHERE legacy_folder_deletion_job_id IS NOT NULL;

-- Claiming looks only at queued jobs whose retry time has arrived. A separate
-- lease index lets a worker reclaim work abandoned by a crashed worker.
CREATE INDEX idx_asset_lifecycle_jobs_claim
    ON asset_lifecycle_jobs (status, next_run_at, queued_at, created_at)
    WHERE status = 'QUEUED';

CREATE INDEX idx_asset_lifecycle_jobs_expired_lease
    ON asset_lifecycle_jobs (lease_expires_at)
    WHERE status = 'RUNNING';

CREATE INDEX idx_asset_lifecycle_jobs_unit_history
    ON asset_lifecycle_jobs (unit_id, created_at DESC);

CREATE OR REPLACE FUNCTION set_asset_lifecycle_job_updated_at()
RETURNS trigger AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_asset_lifecycle_jobs_set_updated_at
BEFORE UPDATE ON asset_lifecycle_jobs
FOR EACH ROW
EXECUTE FUNCTION set_asset_lifecycle_job_updated_at();
