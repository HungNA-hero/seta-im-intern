package repository

import (
	"context"
	"errors"
	"fmt"
	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/eventing/event"
	"seta-im-intern/go-asset-core/internal/eventing/media"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type mediaJobStore struct {
	db       *gorm.DB
	lease    domain.MediaLeasePolicy
	retry    domain.MediaRetryPolicy
	attempts int
}

func NewMediaJobStore(db *gorm.DB, lease domain.MediaLeasePolicy, retry domain.MediaRetryPolicy) *mediaJobStore {
	delays := append([]time.Duration(nil), retry.Delays...)
	return &mediaJobStore{
		db:       db,
		lease:    lease,
		retry:    domain.MediaRetryPolicy{Delays: delays},
		attempts: len(delays) + 1,
	}
}

const mediaProcessingFailedCode = "MEDIA_PROCESSING_FAILED"

var (
	ErrJobNotFound   = errors.New("media processing job does not exist")
	ErrJobTerminal   = errors.New("media processing job already reached terminal state")
	ErrJobIsolated   = errors.New("media processing job is notification-isolated")
	ErrJobLeased     = errors.New("media processing job is leased by another worker")
	ErrJobNotDue     = errors.New("media processing job is not due yet")
	ErrJobExhausted  = errors.New("media processing job has no attempts left")
	ErrLeaseNotHeld  = errors.New("media processing job lease is no longer held")
	ErrJobNotClaimed = errors.New("media processing job could not be claimed")
)

func (store *mediaJobStore) ClaimJob(ctx context.Context, jobID, owner string) (domain.MediaProcessingJob, domain.JobLease, error) {
	if owner == "" {
		return domain.MediaProcessingJob{}, domain.JobLease{}, errors.New("media job claim requires a lease owner")
	}

	var claimed domain.MediaProcessingJob
	err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rows := tx.Raw(`
			UPDATE media_processing_jobs
			SET status = 'processing',
			    stage = 'validating',
			    attempt_count = attempt_count + 1,
			    lease_owner = ?,
			    lease_expires_at = statement_timestamp() + make_interval(secs => ?),
			    started_at = COALESCE(started_at, statement_timestamp())
			WHERE id = ?
			  AND status IN ('queued', 'processing')
			  AND EXISTS (
				SELECT 1 FROM metadata_items AS asset
				WHERE asset.id = media_processing_jobs.asset_id
				  AND asset.deleted_at IS NULL
			  )
			  AND next_attempt_at <= statement_timestamp()
			  AND notification_isolated_at IS NULL
			  AND attempt_count < ?
			  AND (lease_expires_at IS NULL OR lease_expires_at <= statement_timestamp())
			RETURNING *`,
			owner, store.lease.Expiry.Seconds(), jobID, store.attempts,
		).Scan(&claimed)
		if rows.Error != nil {
			return fmt.Errorf("claiming media job %s: %w", jobID, rows.Error)
		}
		if rows.RowsAffected == 1 {
			return nil
		}
		return store.explainRefusal(tx, jobID)
	})
	if err != nil {
		return domain.MediaProcessingJob{}, domain.JobLease{}, err
	}

	lease := domain.JobLease{Owner: owner}
	if claimed.LeaseExpiresAt != nil {
		lease.ExpiresAt = *claimed.LeaseExpiresAt
	}
	return claimed, lease, nil
}

func (store *mediaJobStore) explainRefusal(tx *gorm.DB, jobID string) error {
	var current struct {
		Status                 string
		AttemptCount           int
		NextAttemptAt          time.Time
		LeaseExpiresAt         *time.Time
		NotificationIsolatedAt *time.Time
		DatabaseNow            time.Time
	}
	found := tx.Raw(`
		SELECT status, attempt_count, next_attempt_at, lease_expires_at, notification_isolated_at, statement_timestamp() AS database_now
		FROM media_processing_jobs
		WHERE id = ?`, jobID).Scan(&current)
	if found.Error != nil {
		return fmt.Errorf("reading media job %s after a refused claim: %w", jobID, found.Error)
	}
	if found.RowsAffected == 0 {
		return fmt.Errorf("%w: %s", ErrJobNotFound, jobID)
	}

	switch {
	case current.NotificationIsolatedAt != nil:
		return fmt.Errorf("%w: %s", ErrJobIsolated, jobID)
	case current.Status == string(domain.ProcessingJobCompleted), current.Status == string(domain.ProcessingJobFailed):
		return fmt.Errorf("%w: %s is %s", ErrJobTerminal, jobID, current.Status)
	case current.LeaseExpiresAt != nil && current.LeaseExpiresAt.After(current.DatabaseNow):
		return fmt.Errorf("%w: %s", ErrJobLeased, jobID)
	case current.AttemptCount >= store.attempts:
		return fmt.Errorf("%w: %s used %d of %d", ErrJobExhausted, jobID, current.AttemptCount, store.attempts)
	case current.NextAttemptAt.After(current.DatabaseNow):
		return fmt.Errorf("%w: %s is due at %s", ErrJobNotDue, jobID, current.NextAttemptAt.UTC())
	default:
		return fmt.Errorf("%w: %s is %s", ErrJobNotClaimed, jobID, current.Status)
	}
}

func (store *mediaJobStore) RenewLease(ctx context.Context, jobID string, held domain.JobLease) (domain.JobLease, error) {
	var renewed struct{ LeaseExpiresAt time.Time }
	rows := store.db.WithContext(ctx).Raw(`
		UPDATE media_processing_jobs
		SET lease_expires_at = statement_timestamp() + make_interval(secs => ?)
		WHERE id = ?
		  AND status = 'processing'
		  AND lease_owner = ?
		  AND lease_expires_at = ?
		  AND lease_expires_at > statement_timestamp()
		RETURNING lease_expires_at`,
		store.lease.Expiry.Seconds(), jobID, held.Owner, held.ExpiresAt.UTC(),
	).Scan(&renewed)
	if rows.Error != nil {
		return domain.JobLease{}, fmt.Errorf("renewing the lease on media job %s: %w", jobID, rows.Error)
	}
	if rows.RowsAffected == 0 {
		return domain.JobLease{}, fmt.Errorf("%w: %s", ErrLeaseNotHeld, jobID)
	}
	return domain.JobLease{Owner: held.Owner, ExpiresAt: renewed.LeaseExpiresAt}, nil
}

func (store *mediaJobStore) SettleExecutionFailure(
	ctx context.Context,
	job domain.MediaProcessingJob,
	held domain.JobLease,
) (bool, error) {
	applied := false
	err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, leased, err := lockMediaJobForFailureSettlement(tx, job.ID, held)
		if err != nil || !leased {
			return err
		}
		if current.OrgID != job.OrgID || current.AssetID != job.AssetID ||
			current.VersionID != job.VersionID || current.AttemptCount != job.AttemptCount {
			return fmt.Errorf("%w: claimed media job %s changed before failure settlement", ErrJobNotClaimed, job.ID)
		}

		if current.AttemptCount >= store.attempts {
			if err := failMediaVersion(tx, MediaFailure{
				JobID: current.ID, VersionID: current.VersionID,
				OrgID: current.OrgID, AssetID: current.AssetID,
				ErrorCode: mediaProcessingFailedCode,
			}); err != nil {
				return err
			}
			applied = true
			return nil
		}

		delayIndex := current.AttemptCount - 1
		if delayIndex < 0 || delayIndex >= len(store.retry.Delays) {
			return fmt.Errorf("media job %s attempt %d has no configured retry delay", current.ID, current.AttemptCount)
		}
		var databaseNow time.Time
		if err := tx.Raw("SELECT statement_timestamp()").Scan(&databaseNow).Error; err != nil {
			return fmt.Errorf("reading database time for media job %s: %w", current.ID, err)
		}
		nextAttemptAt := databaseNow.Add(store.retry.Delays[delayIndex])

		result := tx.Exec(`
			UPDATE media_processing_jobs
			SET status = 'queued',
			    stage = NULL,
			    next_attempt_at = ?,
			    last_error_code = ?,
			    lease_owner = NULL,
			    lease_expires_at = NULL
			WHERE id = ?`,
			nextAttemptAt, mediaProcessingFailedCode, current.ID,
		)
		if result.Error != nil {
			return fmt.Errorf("rescheduling media job %s: %w", current.ID, result.Error)
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("%w: rescheduling media job %s", ErrJobNotClaimed, current.ID)
		}
		if err := insertMediaRetryOutbox(ctx, tx, current, databaseNow, nextAttemptAt); err != nil {
			return err
		}
		applied = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("settling the failed execution of media job %s: %w", job.ID, err)
	}
	return applied, nil
}

func lockMediaJobForFailureSettlement(
	tx *gorm.DB,
	jobID string,
	held domain.JobLease,
) (domain.MediaProcessingJob, bool, error) {
	var job domain.MediaProcessingJob
	result := tx.Raw(`
		SELECT *
		FROM media_processing_jobs
		WHERE id = ?
		  AND status = 'processing'
		  AND lease_owner = ?
		  AND lease_expires_at = ?
		  AND lease_expires_at > statement_timestamp()
		FOR UPDATE`,
		jobID, held.Owner, held.ExpiresAt.UTC(),
	).Scan(&job)
	if result.Error != nil {
		return domain.MediaProcessingJob{}, false, fmt.Errorf("locking media job %s for failure settlement: %w", jobID, result.Error)
	}
	return job, result.RowsAffected == 1, nil
}

func insertMediaRetryOutbox(
	ctx context.Context,
	tx *gorm.DB,
	job domain.MediaProcessingJob,
	occurredAt time.Time,
	nextAttemptAt time.Time,
) error {
	var uploadID string
	result := tx.Raw(`
		SELECT upload_id
		FROM asset_media_versions
		WHERE id = ? AND org_id = ? AND asset_id = ?`,
		job.VersionID, job.OrgID, job.AssetID,
	).Scan(&uploadID)
	if result.Error != nil {
		return fmt.Errorf("loading upload identity for retry of media job %s: %w", job.ID, result.Error)
	}
	if result.RowsAffected != 1 || uploadID == "" {
		return fmt.Errorf("%w: version %s for media job %s", ErrVersionNotFound, job.VersionID, job.ID)
	}

	outboxID := uuid.NewString()
	payload, err := media.Marshal(
		event.Envelope{
			EventID: outboxID, OccurredAt: occurredAt,
			OrgID: job.OrgID, Traceparent: event.Traceparent(ctx),
		},
		media.Payload{
			AssetID: job.AssetID, UploadID: uploadID,
			VersionID: job.VersionID, JobID: job.ID,
		},
	)
	if err != nil {
		return fmt.Errorf("building retry event for media job %s: %w", job.ID, err)
	}
	if err := insertOutboxJSON(tx, domain.MediaJobOutboxRecord{
		ID: outboxID, JobID: job.ID,
		EventType:     domain.MediaProcessingRequestedEventType,
		SchemaVersion: media.SchemaVersion, Payload: payload,
		Status: "pending", NextAttemptAt: nextAttemptAt,
		CreatedAt: occurredAt, UpdatedAt: occurredAt,
	}); err != nil {
		return fmt.Errorf("inserting retry event for media job %s: %w", job.ID, err)
	}
	return nil
}
