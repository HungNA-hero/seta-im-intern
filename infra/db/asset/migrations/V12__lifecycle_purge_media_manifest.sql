-- KAN-84: object keys must outlive a failed purge attempt. A lifecycle job
-- remains as the audit/retry owner after its source Asset rows are gone.
CREATE TABLE asset_lifecycle_purge_objects (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    lifecycle_job_id uuid NOT NULL
        REFERENCES asset_lifecycle_jobs(id) ON DELETE RESTRICT,
    org_id uuid NOT NULL REFERENCES organization_ref(org_id) ON DELETE RESTRICT,
    asset_id uuid NOT NULL,
    object_key text NOT NULL,
    deleted_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    CONSTRAINT uq_asset_lifecycle_purge_objects_job_key
        UNIQUE (lifecycle_job_id, object_key)
);

CREATE INDEX idx_asset_lifecycle_purge_objects_pending
    ON asset_lifecycle_purge_objects (lifecycle_job_id, asset_id, created_at)
    WHERE deleted_at IS NULL;
