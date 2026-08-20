package repository

import (
	"context"
	"fmt"
	"time"
)

type MediaQuotaSnapshot struct {
	OrganizationID string
	ConsumedBytes  int64
	QuotaBytes     int64
}

type MediaOperationalSnapshot struct {
	QueueOldestAge          time.Duration
	OutboxOldestAge         time.Duration
	ReconciliationOldestAge time.Duration
	CleanupBacklog          int64
	QuarantineBacklog       int64
	QuarantineOldestAge     time.Duration
	Quota                   []MediaQuotaSnapshot
}

// GetMediaOperationalSnapshot reads bounded aggregate state from PostgreSQL.
// IDs are returned only for the quota alert log; callers must never turn them
// into metric labels.
func (store *mediaJobStore) GetMediaOperationalSnapshot(
	ctx context.Context,
	quarantine time.Duration,
) (MediaOperationalSnapshot, error) {
	var row struct {
		QueueOldestSeconds          float64
		OutboxOldestSeconds         float64
		ReconciliationOldestSeconds float64
		CleanupBacklog              int64
		QuarantineBacklog           int64
		QuarantineOldestSeconds     float64
	}
	err := store.db.WithContext(ctx).Raw(`
		SELECT
		  COALESCE((
		    SELECT EXTRACT(EPOCH FROM (statement_timestamp() - MIN(queued_at)))
		    FROM media_processing_jobs
		    WHERE status = 'queued' AND next_attempt_at <= statement_timestamp()
		      AND notification_isolated_at IS NULL
		  ), 0) AS queue_oldest_seconds,
		  COALESCE((
		    SELECT EXTRACT(EPOCH FROM (statement_timestamp() - MIN(created_at)))
		    FROM media_job_outbox
		    WHERE status IN ('pending', 'publishing')
		  ), 0) AS outbox_oldest_seconds,
		  COALESCE((
		    SELECT EXTRACT(EPOCH FROM (statement_timestamp() - MIN(published.published_at)))
		    FROM media_processing_jobs AS jobs
		    JOIN LATERAL (
		      SELECT max(published_at) AS published_at
		      FROM media_job_outbox
		      WHERE job_id = jobs.id AND status = 'published' AND published_at IS NOT NULL
		    ) AS published ON published.published_at IS NOT NULL
		    WHERE jobs.status IN ('queued', 'processing')
		      AND jobs.notification_isolated_at IS NULL
		      AND NOT EXISTS (
		        SELECT 1 FROM media_job_outbox AS pending
		        WHERE pending.job_id = jobs.id AND pending.status IN ('pending', 'publishing')
		      )
		  ), 0) AS reconciliation_oldest_seconds,
		  (SELECT count(*)
		   FROM media_upload_sessions AS sessions
		   LEFT JOIN asset_media_versions AS versions ON versions.upload_id = sessions.id
		   WHERE sessions.raw_object_purged_at IS NULL
		     AND ((versions.id IS NULL
		           AND sessions.state IN ('expired', 'cancelled', 'failed')
		           AND COALESCE(sessions.cancelled_at, sessions.expired_at) <= statement_timestamp() - ?::interval)
		       OR (versions.status = 'failed'
		           AND versions.failed_at <= statement_timestamp() - ?::interval))) AS cleanup_backlog,
		  (SELECT count(*) FROM media_processing_jobs WHERE notification_isolated_at IS NOT NULL) AS quarantine_backlog,
		  COALESCE((
		    SELECT EXTRACT(EPOCH FROM (statement_timestamp() - MIN(notification_isolated_at)))
		    FROM media_processing_jobs WHERE notification_isolated_at IS NOT NULL
		  ), 0) AS quarantine_oldest_seconds`, quarantine.String(), quarantine.String()).Scan(&row).Error
	if err != nil {
		return MediaOperationalSnapshot{}, fmt.Errorf("reading media operational backlog: %w", err)
	}

	var quotas []MediaQuotaSnapshot
	if err := store.db.WithContext(ctx).Raw(`
		SELECT org_id::text AS organization_id,
		       reserved_raw_bytes + stored_raw_bytes AS consumed_bytes,
		       raw_quota_bytes AS quota_bytes
		FROM organization_media_usage`).Scan(&quotas).Error; err != nil {
		return MediaOperationalSnapshot{}, fmt.Errorf("reading media quota headroom: %w", err)
	}
	return MediaOperationalSnapshot{
		QueueOldestAge:          time.Duration(row.QueueOldestSeconds * float64(time.Second)),
		OutboxOldestAge:         time.Duration(row.OutboxOldestSeconds * float64(time.Second)),
		ReconciliationOldestAge: time.Duration(row.ReconciliationOldestSeconds * float64(time.Second)),
		CleanupBacklog:          row.CleanupBacklog,
		QuarantineBacklog:       row.QuarantineBacklog,
		QuarantineOldestAge:     time.Duration(row.QuarantineOldestSeconds * float64(time.Second)),
		Quota:                   quotas,
	}, nil
}
