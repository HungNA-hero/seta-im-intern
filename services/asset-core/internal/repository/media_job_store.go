package repository

import (
	"context"
	"errors"
	"fmt"
	"seta-im-intern/go-asset-core/internal/domain"
	"time"

	"gorm.io/gorm"
)

type mediaJobStore struct {
	db       *gorm.DB
	lease    domain.MediaLeasePolicy
	attempts int
}

func NewMediaJobStore(db *gorm.DB, lease domain.MediaLeasePolicy, maxAttempts int) *mediaJobStore {
	return &mediaJobStore{db: db, lease: lease, attempts: maxAttempts}
}

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

func (store *mediaJobStore) ReleaseLease(ctx context.Context, jobID string, held domain.JobLease) (bool, error) {
	result := store.db.WithContext(ctx).Exec(`
		UPDATE media_processing_jobs
		SET status = 'queued',
		    stage = NULL,
		    lease_owner = NULL,
		    lease_expires_at = NULL
		WHERE id = ?
		  AND status = 'processing'
		  AND lease_owner = ?
		  AND lease_expires_at = ?
		  AND lease_expires_at > statement_timestamp()`,
		jobID, held.Owner, held.ExpiresAt.UTC(),
	)
	if result.Error != nil {
		return false, fmt.Errorf("releasing the lease on media job %s: %w", jobID, result.Error)
	}
	return result.RowsAffected == 1, nil
}
