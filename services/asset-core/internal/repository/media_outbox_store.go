package repository

import (
	"context"
	"errors"
	"fmt"
	"seta-im-intern/go-asset-core/internal/eventing/outbox"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type mediaOutboxStore struct {
	db       *gorm.DB
	topic    string
	leaseTTL time.Duration
}

type MediaOutboxStoreOptions struct {
	Topic    string
	LeaseTTL time.Duration
}

const defaultMediaOutboxLeaseTTL = 30 * time.Second

var ErrMediaOutboxTopicRequired = errors.New("media outbox topic is required")

func NewMediaOutboxStore(db *gorm.DB, options MediaOutboxStoreOptions) (*mediaOutboxStore, error) {
	if options.Topic == "" {
		return nil, ErrMediaOutboxTopicRequired
	}
	if options.LeaseTTL <= 0 {
		options.LeaseTTL = defaultMediaOutboxLeaseTTL
	}
	return &mediaOutboxStore{db: db, topic: options.Topic, leaseTTL: options.LeaseTTL}, nil
}

type claimedRow struct {
	ID             string
	JobID          string
	Payload        []byte
	AttemptCount   int
	CreatedAt      time.Time
	LeaseExpiresAt time.Time
}

func (store *mediaOutboxStore) Claim(ctx context.Context, owner string, limit int) (outbox.Claimed, error) {
	if owner == "" {
		return outbox.Claimed{}, errors.New("media outbox claim requires a lease owner")
	}
	if limit <= 0 {
		return outbox.Claimed{}, nil
	}

	var rows []claimedRow
	err := store.db.WithContext(ctx).Raw(`
		UPDATE media_job_outbox
		SET status = 'publishing',
		    lease_owner = ?,
		    lease_expires_at = statement_timestamp() + make_interval(secs => ?)
		WHERE id IN (
			SELECT id
			FROM media_job_outbox
			WHERE status IN ('pending', 'publishing')
			  AND next_attempt_at <= statement_timestamp()
			  AND (lease_expires_at IS NULL OR lease_expires_at <= statement_timestamp())
			ORDER BY created_at, id
			LIMIT ?
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, job_id, payload, attempt_count, created_at, lease_expires_at`,
		owner, store.leaseTTL.Seconds(), limit,
	).Scan(&rows).Error
	if err != nil {
		return outbox.Claimed{}, fmt.Errorf("claiming media outbox rows for %q: %w", owner, err)
	}
	if len(rows) == 0 {
		return outbox.Claimed{}, nil
	}

	claimed := outbox.Claimed{
		Records: make([]outbox.Record, 0, len(rows)),
		Lease:   outbox.Lease{Owner: owner, ExpiresAt: rows[0].LeaseExpiresAt},
	}
	for _, row := range rows {
		eventID, parseErr := uuid.Parse(row.ID)
		if parseErr != nil {
			return outbox.Claimed{}, fmt.Errorf("media outbox row %q has an unusable event ID: %w", row.ID, parseErr)
		}
		claimed.Records = append(claimed.Records, outbox.Record{
			EventID:      eventID,
			Topic:        store.topic,
			Key:          row.JobID,
			Payload:      row.Payload,
			AttemptCount: row.AttemptCount,
			EnqueuedAt:   row.CreatedAt,
		})
	}
	return claimed, nil
}

func (store *mediaOutboxStore) MarkPublished(ctx context.Context, eventID uuid.UUID, lease outbox.Lease, at time.Time) (bool, error) {
	result := store.db.WithContext(ctx).Exec(`
		UPDATE media_job_outbox
		SET status = 'published',
		    published_at = ?,
		    lease_owner = NULL,
		    lease_expires_at = NULL
		WHERE id = ?
		  AND status = 'publishing'
		  AND lease_owner = ?
		  AND lease_expires_at = ?
		  AND lease_expires_at > statement_timestamp()`,
		at.UTC(), eventID.String(), lease.Owner, lease.ExpiresAt.UTC(),
	)
	if result.Error != nil {
		return false, fmt.Errorf("marking media outbox event %s published: %w", eventID, result.Error)
	}
	return result.RowsAffected == 1, nil
}

func (store *mediaOutboxStore) Reschedule(
	ctx context.Context,
	eventID uuid.UUID,
	lease outbox.Lease,
	nextAttemptAt time.Time,
	attemptCount int,
	errorCode string,
) (bool, error) {
	result := store.db.WithContext(ctx).Exec(`
		UPDATE media_job_outbox
		SET status = 'pending',
		    next_attempt_at = ?,
		    attempt_count = ?,
		    last_error_code = ?,
		    lease_owner = NULL,
		    lease_expires_at = NULL
		WHERE id = ?
		  AND status = 'publishing'
		  AND lease_owner = ?
		  AND lease_expires_at = ?
		  AND lease_expires_at > statement_timestamp()`,
		nextAttemptAt.UTC(), attemptCount, truncateErrorCode(errorCode), eventID.String(), lease.Owner, lease.ExpiresAt.UTC(),
	)
	if result.Error != nil {
		return false, fmt.Errorf("rescheduling media outbox event %s: %w", eventID, result.Error)
	}
	return result.RowsAffected == 1, nil
}

func truncateErrorCode(errorCode string) string {
	const maxErrorCodeLength = 64
	if len(errorCode) <= maxErrorCodeLength {
		return errorCode
	}
	return errorCode[:maxErrorCodeLength]
}
