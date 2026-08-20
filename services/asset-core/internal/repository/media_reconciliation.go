package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/eventing/event"
	"seta-im-intern/go-asset-core/internal/eventing/media"
)

var ErrInvalidMediaReconciliation = errors.New("invalid media reconciliation request")

type staleMediaDispatch struct {
	JobID           string
	OrgID           string
	AssetID         string
	VersionID       string
	UploadID        string
	LastPublishedAt time.Time
}

func (store *mediaJobStore) ReconcileStaleDispatches(
	ctx context.Context,
	batchSize int,
	staleAfter time.Duration,
) (int, error) {
	if batchSize <= 0 || staleAfter <= 0 {
		return 0, ErrInvalidMediaReconciliation
	}

	inserted := 0
	err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var databaseNow time.Time
		if err := tx.Raw("SELECT statement_timestamp()").Scan(&databaseNow).Error; err != nil {
			return fmt.Errorf("reading database time for media reconciliation: %w", err)
		}

		var candidates []staleMediaDispatch
		result := tx.Raw(`
			SELECT jobs.id AS job_id,
			       jobs.org_id,
			       jobs.asset_id,
			       jobs.version_id,
			       versions.upload_id,
			       latest.last_published_at
			FROM media_processing_jobs AS jobs
			JOIN asset_media_versions AS versions
			  ON versions.id = jobs.version_id
			JOIN LATERAL (
				SELECT max(published_at) AS last_published_at
				FROM media_job_outbox
				WHERE job_id = jobs.id
				  AND status = 'published'
				  AND published_at IS NOT NULL
			) AS latest ON latest.last_published_at IS NOT NULL
			WHERE jobs.notification_isolated_at IS NULL
			  AND (
				(jobs.status = 'queued' AND jobs.next_attempt_at <= ?)
				OR
				(jobs.status = 'processing' AND jobs.lease_expires_at <= ?)
			  )
			  AND latest.last_published_at <= ?
			  AND NOT EXISTS (
				SELECT 1
				FROM media_job_outbox AS unpublished
				WHERE unpublished.job_id = jobs.id
				  AND unpublished.status IN ('pending', 'publishing')
			  )
			ORDER BY latest.last_published_at, jobs.id
			LIMIT ?
			FOR UPDATE OF jobs SKIP LOCKED`,
			databaseNow,
			databaseNow,
			databaseNow.Add(-staleAfter),
			batchSize,
		).Scan(&candidates)
		if result.Error != nil {
			return fmt.Errorf("selecting stale media dispatches: %w", result.Error)
		}

		for _, candidate := range candidates {
			eventID := uuid.NewString()
			payload, err := media.Marshal(
				event.Envelope{
					EventID:    eventID,
					OccurredAt: databaseNow.UTC(),
					OrgID:      candidate.OrgID,
				},
				media.Payload{
					AssetID:   candidate.AssetID,
					UploadID:  candidate.UploadID,
					VersionID: candidate.VersionID,
					JobID:     candidate.JobID,
				},
			)
			if err != nil {
				return fmt.Errorf("building reconciliation event for media job %s: %w", candidate.JobID, err)
			}

			created, err := insertReconciliationOutbox(tx, domain.MediaJobOutboxRecord{
				ID:            eventID,
				JobID:         candidate.JobID,
				EventType:     media.EventType,
				SchemaVersion: media.SchemaVersion,
				Payload:       payload,
				Status:        "pending",
				NextAttemptAt: databaseNow,
				CreatedAt:     databaseNow,
				UpdatedAt:     databaseNow,
			})
			if err != nil {
				return err
			}
			if created {
				inserted++
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return inserted, nil
}

func insertReconciliationOutbox(tx *gorm.DB, record domain.MediaJobOutboxRecord) (bool, error) {
	result := tx.Exec(`
		INSERT INTO media_job_outbox
			(id, job_id, event_type, schema_version, payload, status, attempt_count,
			 next_attempt_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?::jsonb, ?, 0, ?, ?, ?)
		ON CONFLICT DO NOTHING`,
		record.ID,
		record.JobID,
		record.EventType,
		record.SchemaVersion,
		string(record.Payload),
		record.Status,
		record.NextAttemptAt,
		record.CreatedAt,
		record.UpdatedAt,
	)
	if result.Error != nil {
		return false, fmt.Errorf("inserting reconciliation event for media job %s: %w", record.JobID, result.Error)
	}
	return result.RowsAffected == 1, nil
}
