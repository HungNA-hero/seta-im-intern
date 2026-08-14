-- KAN-85 / Spec 006: media upload acceptance and processing state.
-- Asset Core owns every table here. Access Core adds no media schema; its
-- caller-rate limits are ephemeral Redis keys, not durable state.

-- One quota ledger row per organization. It meters raw archived bytes only;
-- processed output overhead is measured separately and does not consume the
-- user-facing quota.
CREATE TABLE organization_media_usage (
    org_id uuid PRIMARY KEY REFERENCES organization_ref(org_id) ON DELETE RESTRICT,
    raw_quota_bytes bigint NOT NULL DEFAULT 10737418240,
    reserved_raw_bytes bigint NOT NULL DEFAULT 0,
    stored_raw_bytes bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_organization_media_usage_quota_positive CHECK (raw_quota_bytes > 0),
    CONSTRAINT chk_organization_media_usage_reserved_non_negative CHECK (reserved_raw_bytes >= 0),
    CONSTRAINT chk_organization_media_usage_stored_non_negative CHECK (stored_raw_bytes >= 0),
    CONSTRAINT chk_organization_media_usage_within_quota CHECK (
        reserved_raw_bytes + stored_raw_bytes <= raw_quota_bytes
    )
);

-- One admitted transfer operation. Owns retry identity before a media version
-- exists, so a client retry key resolves without touching version state.
CREATE TABLE media_upload_sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organization_ref(org_id) ON DELETE RESTRICT,
    asset_id uuid NOT NULL REFERENCES metadata_items(id) ON DELETE RESTRICT,
    requested_by uuid NOT NULL REFERENCES user_ref(user_id) ON DELETE RESTRICT,
    idempotency_key uuid NOT NULL,
    request_fingerprint bytea NOT NULL,
    state varchar(20) NOT NULL,
    original_filename varchar(255) NOT NULL,
    declared_content_type varchar(32) NOT NULL,
    file_extension varchar(5) NOT NULL,
    expected_size_bytes bigint NOT NULL,
    declared_checksum_sha256 bytea NOT NULL,
    raw_object_key text NOT NULL,
    credential_expires_at timestamptz NOT NULL,
    session_expires_at timestamptz NOT NULL,
    committed_at timestamptz,
    expired_at timestamptz,
    cancelled_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_media_upload_sessions_state CHECK (
        state IN ('created', 'committed', 'expired', 'cancelled', 'failed')
    ),
    CONSTRAINT chk_media_upload_sessions_fingerprint_length CHECK (
        octet_length(request_fingerprint) = 32
    ),
    CONSTRAINT chk_media_upload_sessions_checksum_length CHECK (
        octet_length(declared_checksum_sha256) = 32
    ),
    CONSTRAINT chk_media_upload_sessions_size_range CHECK (
        expected_size_bytes BETWEEN 1 AND 50000000
    ),
    CONSTRAINT chk_media_upload_sessions_type_extension CHECK (
        (declared_content_type = 'image/jpeg' AND file_extension = 'jpg')
        OR (declared_content_type = 'image/png' AND file_extension = 'png')
    ),
    CONSTRAINT chk_media_upload_sessions_filename_present CHECK (
        length(original_filename) > 0
    )
);

CREATE UNIQUE INDEX uq_media_upload_sessions_idempotency
    ON media_upload_sessions (org_id, asset_id, idempotency_key);

CREATE UNIQUE INDEX uq_media_upload_sessions_raw_object_key
    ON media_upload_sessions (raw_object_key);

-- One open session per asset. Partial on 'created' so a cancelled session frees
-- the asset immediately instead of waiting for session_expires_at.
CREATE UNIQUE INDEX uq_media_upload_sessions_open_per_asset
    ON media_upload_sessions (asset_id)
    WHERE state = 'created';

-- Covers 'cancelled' as well as 'created', so the abandoned-artifact loop
-- reclaims a cancelled session's object on the same schedule as an expired one.
CREATE INDEX idx_media_upload_sessions_cleanup
    ON media_upload_sessions (state, session_expires_at);

-- One immutable candidate or replacement operation and its validation outcome.
CREATE TABLE asset_media_versions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organization_ref(org_id) ON DELETE RESTRICT,
    asset_id uuid NOT NULL REFERENCES metadata_items(id) ON DELETE RESTRICT,
    upload_id uuid NOT NULL REFERENCES media_upload_sessions(id) ON DELETE RESTRICT,
    requested_by uuid NOT NULL REFERENCES user_ref(user_id) ON DELETE RESTRICT,
    status varchar(20) NOT NULL,
    raw_object_key text NOT NULL,
    raw_retained boolean NOT NULL DEFAULT true,
    declared_content_type varchar(32) NOT NULL,
    detected_content_type varchar(32),
    original_size_bytes bigint NOT NULL,
    sha256 bytea,
    source_width integer,
    source_height integer,
    failure_code varchar(64),
    processing_started_at timestamptz,
    completed_at timestamptz,
    failed_at timestamptz,
    activated_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_asset_media_versions_status CHECK (
        status IN ('pending', 'processing', 'completed', 'failed')
    ),
    CONSTRAINT chk_asset_media_versions_sha256_length CHECK (
        sha256 IS NULL OR octet_length(sha256) = 32
    ),
    CONSTRAINT chk_asset_media_versions_detected_type CHECK (
        detected_content_type IS NULL OR detected_content_type IN ('image/jpeg', 'image/png')
    ),
    CONSTRAINT chk_asset_media_versions_dimensions CHECK (
        (source_width IS NULL AND source_height IS NULL)
        OR (source_width IS NOT NULL AND source_height IS NOT NULL AND source_width > 0 AND source_height > 0)
    ),
    CONSTRAINT chk_asset_media_versions_size_positive CHECK (original_size_bytes > 0)
);

CREATE UNIQUE INDEX uq_asset_media_versions_upload
    ON asset_media_versions (upload_id);

CREATE UNIQUE INDEX uq_asset_media_versions_raw_object_key
    ON asset_media_versions (raw_object_key);

CREATE INDEX idx_asset_media_versions_status_history
    ON asset_media_versions (org_id, asset_id, status, created_at DESC);

-- Trusted metadata for the fixed processed objects. Exactly two rows per
-- completed version, both produced directly from the raw original.
CREATE TABLE media_outputs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    version_id uuid NOT NULL REFERENCES asset_media_versions(id) ON DELETE RESTRICT,
    kind varchar(16) NOT NULL,
    object_key text NOT NULL,
    content_type varchar(32) NOT NULL,
    width integer NOT NULL,
    height integer NOT NULL,
    size_bytes bigint NOT NULL,
    sha256 bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_media_outputs_kind CHECK (kind IN ('thumbnail', 'web')),
    CONSTRAINT chk_media_outputs_content_type CHECK (
        content_type IN ('image/jpeg', 'image/png')
    ),
    CONSTRAINT chk_media_outputs_dimensions_positive CHECK (width > 0 AND height > 0),
    CONSTRAINT chk_media_outputs_size_positive CHECK (size_bytes > 0),
    CONSTRAINT chk_media_outputs_sha256_length CHECK (octet_length(sha256) = 32),
    CONSTRAINT chk_media_outputs_bounding_box CHECK (
        (kind = 'thumbnail' AND width <= 256 AND height <= 256)
        OR (kind = 'web' AND width <= 1080 AND height <= 1080)
    )
);

CREATE UNIQUE INDEX uq_media_outputs_version_kind
    ON media_outputs (version_id, kind);

CREATE UNIQUE INDEX uq_media_outputs_object_key
    ON media_outputs (object_key);

-- Durable source of truth for asynchronous processing and retry ownership.
-- Kafka is notification transport only; this table is authoritative.
CREATE TABLE media_processing_jobs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organization_ref(org_id) ON DELETE RESTRICT,
    asset_id uuid NOT NULL REFERENCES metadata_items(id) ON DELETE RESTRICT,
    version_id uuid NOT NULL REFERENCES asset_media_versions(id) ON DELETE RESTRICT,
    status varchar(20) NOT NULL,
    stage varchar(20),
    attempt_count integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    lease_owner text,
    lease_expires_at timestamptz,
    last_error_code varchar(64),
    notification_isolated_at timestamptz,
    queued_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    completed_at timestamptz,
    failed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_media_processing_jobs_status CHECK (
        status IN ('queued', 'processing', 'completed', 'failed')
    ),
    CONSTRAINT chk_media_processing_jobs_stage CHECK (
        stage IS NULL OR stage IN ('validating', 'transforming')
    ),
    CONSTRAINT chk_media_processing_jobs_attempt_range CHECK (
        attempt_count BETWEEN 0 AND 3
    ),
    CONSTRAINT chk_media_processing_jobs_isolation CHECK (
        notification_isolated_at IS NULL
        OR (status = 'failed' AND last_error_code = 'MEDIA_NOTIFICATION_ISOLATED')
    )
);

CREATE UNIQUE INDEX uq_media_processing_jobs_version
    ON media_processing_jobs (version_id);

CREATE INDEX idx_media_processing_jobs_claim
    ON media_processing_jobs (status, next_attempt_at, queued_at);

CREATE INDEX idx_media_processing_jobs_expired_lease
    ON media_processing_jobs (lease_expires_at)
    WHERE status = 'processing' AND notification_isolated_at IS NULL;

CREATE INDEX idx_media_processing_jobs_asset_status
    ON media_processing_jobs (org_id, asset_id, created_at DESC);

-- Media-owned outbox. Rows are written in the same transaction as the business
-- mutation; the shared KAN-81 relay publishes them and marks them published only
-- after the broker acknowledges. Not shared with lifecycle events.
CREATE TABLE media_job_outbox (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id uuid NOT NULL REFERENCES media_processing_jobs(id) ON DELETE RESTRICT,
    event_type varchar(64) NOT NULL,
    schema_version smallint NOT NULL DEFAULT 1,
    payload jsonb NOT NULL,
    status varchar(16) NOT NULL DEFAULT 'pending',
    attempt_count integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    lease_owner text,
    lease_expires_at timestamptz,
    last_error_code varchar(64),
    published_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_media_job_outbox_status CHECK (
        status IN ('pending', 'publishing', 'published')
    ),
    CONSTRAINT chk_media_job_outbox_attempt_non_negative CHECK (attempt_count >= 0),
    CONSTRAINT chk_media_job_outbox_schema_version_positive CHECK (schema_version > 0)
);

CREATE INDEX idx_media_job_outbox_claim
    ON media_job_outbox (status, next_attempt_at, created_at);

CREATE INDEX idx_media_job_outbox_expired_lease
    ON media_job_outbox (lease_expires_at)
    WHERE status = 'publishing';

-- At most one unpublished event per job. This is what lets the shared relay's
-- per-key claim rule hold, and what makes concurrent reconcilers converge on a
-- single unpublished event.
CREATE UNIQUE INDEX uq_media_job_outbox_unpublished_job
    ON media_job_outbox (job_id)
    WHERE status IN ('pending', 'publishing');

-- Media pointers on the existing asset row. Both directions are RESTRICT, so a
-- purge must null these first; the documented teardown order depends on it.
ALTER TABLE metadata_items
    ADD COLUMN active_media_version_id uuid REFERENCES asset_media_versions(id) ON DELETE RESTRICT,
    ADD COLUMN pending_media_version_id uuid REFERENCES asset_media_versions(id) ON DELETE RESTRICT,
    ADD CONSTRAINT chk_metadata_items_distinct_media_pointers CHECK (
        active_media_version_id IS NULL
        OR pending_media_version_id IS NULL
        OR active_media_version_id <> pending_media_version_id
    );

-- One pending version cannot be attached to more than one asset.
CREATE UNIQUE INDEX uq_metadata_items_pending_media_version
    ON metadata_items (pending_media_version_id)
    WHERE pending_media_version_id IS NOT NULL;

CREATE INDEX idx_metadata_items_active_media_version
    ON metadata_items (active_media_version_id)
    WHERE active_media_version_id IS NOT NULL;

-- The V1 set_updated_at() deliberately skips the bump on a soft delete, so it
-- reads OLD.deleted_at and can only be attached to tables that have that
-- column. Media tables are hard-deleted by the documented teardown order and
-- have no deleted_at, so they need their own unconditional bump.
CREATE OR REPLACE FUNCTION set_media_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_organization_media_usage_set_updated_at
BEFORE UPDATE ON organization_media_usage
FOR EACH ROW
EXECUTE FUNCTION set_media_updated_at();

CREATE TRIGGER trg_media_upload_sessions_set_updated_at
BEFORE UPDATE ON media_upload_sessions
FOR EACH ROW
EXECUTE FUNCTION set_media_updated_at();

CREATE TRIGGER trg_asset_media_versions_set_updated_at
BEFORE UPDATE ON asset_media_versions
FOR EACH ROW
EXECUTE FUNCTION set_media_updated_at();

CREATE TRIGGER trg_media_processing_jobs_set_updated_at
BEFORE UPDATE ON media_processing_jobs
FOR EACH ROW
EXECUTE FUNCTION set_media_updated_at();

CREATE TRIGGER trg_media_job_outbox_set_updated_at
BEFORE UPDATE ON media_job_outbox
FOR EACH ROW
EXECUTE FUNCTION set_media_updated_at();
