-- KAN-84: scheduler coordination for the daily retention-cleanup workflow.
-- A scheduler acquires this short lease before it creates the one durable run
-- record for a calendar date. The scheduler does not delete Asset rows itself.
CREATE TABLE asset_lifecycle_scheduler_leases (
    scheduler_name varchar(64) PRIMARY KEY,
    lease_owner text,
    lease_expires_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_asset_lifecycle_scheduler_leases_owner_expiry CHECK (
        (lease_owner IS NULL AND lease_expires_at IS NULL)
        OR (lease_owner IS NOT NULL AND lease_expires_at IS NOT NULL)
    )
);

CREATE OR REPLACE FUNCTION set_asset_lifecycle_scheduler_lease_updated_at()
RETURNS trigger AS $$
BEGIN
    NEW.updated_at = statement_timestamp();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_asset_lifecycle_scheduler_leases_set_updated_at
BEFORE UPDATE ON asset_lifecycle_scheduler_leases
FOR EACH ROW
EXECUTE FUNCTION set_asset_lifecycle_scheduler_lease_updated_at();

CREATE TABLE asset_lifecycle_cleanup_runs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    scheduler_name varchar(64) NOT NULL
        REFERENCES asset_lifecycle_scheduler_leases(scheduler_name) ON DELETE RESTRICT,
    run_date date NOT NULL,
    timezone varchar(64) NOT NULL,
    status varchar(16) NOT NULL DEFAULT 'QUEUED',
    checkpoint jsonb NOT NULL DEFAULT '{}'::jsonb,
    failure_code varchar(64),
    started_at timestamptz,
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_asset_lifecycle_cleanup_runs_scheduler_date
        UNIQUE (scheduler_name, run_date),
    CONSTRAINT chk_asset_lifecycle_cleanup_runs_status CHECK (
        status IN ('QUEUED', 'RUNNING', 'SUCCEEDED', 'FAILED')
    ),
    CONSTRAINT chk_asset_lifecycle_cleanup_runs_checkpoint CHECK (
        jsonb_typeof(checkpoint) = 'object'
    )
);

CREATE INDEX idx_asset_lifecycle_cleanup_runs_claim
    ON asset_lifecycle_cleanup_runs (status, run_date, created_at)
    WHERE status = 'QUEUED';

-- A scheduled retention purge is performed by the service, not by the user
-- who originally deleted the Asset. Keep that distinction explicit instead of
-- inventing a user_ref solely to satisfy the old audit column.
ALTER TABLE asset_lifecycle_jobs
    ADD COLUMN initiated_by_type varchar(16) NOT NULL DEFAULT 'USER',
    ALTER COLUMN requested_by DROP NOT NULL,
    ADD CONSTRAINT chk_asset_lifecycle_jobs_initiator_type CHECK (
        initiated_by_type IN ('USER', 'SYSTEM')
    ),
    ADD CONSTRAINT chk_asset_lifecycle_jobs_initiator_identity CHECK (
        (initiated_by_type = 'USER' AND requested_by IS NOT NULL)
        OR (initiated_by_type = 'SYSTEM' AND requested_by IS NULL)
    );

CREATE OR REPLACE FUNCTION set_asset_lifecycle_cleanup_run_updated_at()
RETURNS trigger AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_asset_lifecycle_cleanup_runs_set_updated_at
BEFORE UPDATE ON asset_lifecycle_cleanup_runs
FOR EACH ROW
EXECUTE FUNCTION set_asset_lifecycle_cleanup_run_updated_at();
