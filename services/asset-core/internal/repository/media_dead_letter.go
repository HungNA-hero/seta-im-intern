package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/eventing/consume"
	"seta-im-intern/go-asset-core/internal/eventing/event"
	"seta-im-intern/go-asset-core/internal/eventing/media"
	"seta-im-intern/go-asset-core/internal/eventing/outbox"
)

const mediaNotificationIsolatedCode = "MEDIA_NOTIFICATION_ISOLATED"

var ErrReplayOperatorUnknown = errors.New("media replay operator is not recognized")

// IsolateNotification commits the database half of quarantine before the
// caller attempts to publish a dead-letter record. The transition deliberately
// leaves the pending version and asset pointer untouched: this is transport
// isolation, not a processing failure.
func (store *mediaJobStore) IsolateNotification(ctx context.Context, record consume.QuarantinedRecord) error {
	result := store.db.WithContext(ctx).Exec(`
		UPDATE media_processing_jobs
		SET status = 'failed',
		    stage = NULL,
		    lease_owner = NULL,
		    lease_expires_at = NULL,
		    last_error_code = ?,
		    notification_isolated_at = statement_timestamp(),
		    failed_at = COALESCE(failed_at, statement_timestamp())
		WHERE id = ?
		  AND status IN ('queued', 'processing')
		  AND notification_isolated_at IS NULL`,
		mediaNotificationIsolatedCode,
		record.AggregateID,
	)
	if result.Error != nil {
		return fmt.Errorf("isolating media notification for job %s: %w", record.AggregateID, result.Error)
	}
	return nil
}

func (store *mediaJobStore) RebuildAndEnqueue(ctx context.Context, request outbox.ReplayRequest) (uuid.UUID, error) {
	jobID, err := uuid.Parse(request.JobID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid replay job identity: %w", err)
	}
	operatorID, err := uuid.Parse(request.Operator)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid replay operator identity: %w", err)
	}

	eventID := uuid.New()
	err = store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job domain.MediaProcessingJob
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", jobID).Take(&job)
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return outbox.ErrNotIsolated
		}
		if result.Error != nil {
			return fmt.Errorf("locking replay job %s: %w", jobID, result.Error)
		}
		if job.NotificationIsolatedAt == nil || job.LastErrorCode == nil ||
			*job.LastErrorCode != mediaNotificationIsolatedCode {
			return outbox.ErrNotIsolated
		}

		var operatorExists bool
		if err := tx.Raw("SELECT EXISTS (SELECT 1 FROM user_ref WHERE user_id = ?)", operatorID).
			Scan(&operatorExists).Error; err != nil {
			return fmt.Errorf("authenticating replay operator: %w", err)
		}
		if !operatorExists {
			return ErrReplayOperatorUnknown
		}

		var version domain.AssetMediaVersion
		if err := tx.Where("id = ? AND org_id = ? AND asset_id = ?", job.VersionID, job.OrgID, job.AssetID).
			Take(&version).Error; err != nil {
			return fmt.Errorf("loading current media truth for replay job %s: %w", job.ID, err)
		}
		var now time.Time
		if err := tx.Raw("SELECT statement_timestamp()").Scan(&now).Error; err != nil {
			return fmt.Errorf("reading replay transaction time: %w", err)
		}
		now = now.UTC()

		payload, err := media.Marshal(
			event.Envelope{
				EventID: eventID.String(), OccurredAt: now, OrgID: job.OrgID,
			},
			media.Payload{
				AssetID: job.AssetID, UploadID: version.UploadID,
				VersionID: job.VersionID, JobID: job.ID,
			},
		)
		if err != nil {
			return fmt.Errorf("building current replay event for job %s: %w", job.ID, err)
		}

		update := tx.Model(&domain.MediaProcessingJob{}).
			Where("id = ? AND notification_isolated_at IS NOT NULL AND last_error_code = ?", job.ID, mediaNotificationIsolatedCode).
			Updates(map[string]any{
				"status":                   domain.ProcessingJobQueued,
				"stage":                    nil,
				"lease_owner":              nil,
				"lease_expires_at":         nil,
				"next_attempt_at":          now,
				"last_error_code":          nil,
				"notification_isolated_at": nil,
				"failed_at":                nil,
				"queued_at":                now,
			})
		if update.Error != nil {
			return fmt.Errorf("restoring isolated media job %s: %w", job.ID, update.Error)
		}
		if update.RowsAffected != 1 {
			return outbox.ErrNotIsolated
		}

		if result := tx.Model(&domain.MetadataItem{}).
			Where("id = ?", job.AssetID).
			Update("updated_by", operatorID.String()); result.Error != nil || result.RowsAffected != 1 {
			if result.Error != nil {
				return fmt.Errorf("auditing replay operator for asset %s: %w", job.AssetID, result.Error)
			}
			return fmt.Errorf("auditing replay operator for asset %s: asset not found", job.AssetID)
		}

		if err := insertOutboxJSON(tx, domain.MediaJobOutboxRecord{
			ID:            eventID.String(),
			JobID:         job.ID,
			EventType:     media.EventType,
			SchemaVersion: media.SchemaVersion,
			Payload:       payload,
			Status:        "pending",
			NextAttemptAt: now,
			CreatedAt:     now,
			UpdatedAt:     now,
		}); err != nil {
			return fmt.Errorf("inserting replay event for media job %s: %w", job.ID, err)
		}
		return nil
	})
	if err != nil {
		return uuid.Nil, err
	}
	return eventID, nil
}
