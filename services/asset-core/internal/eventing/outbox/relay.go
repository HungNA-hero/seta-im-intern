package outbox

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"
)

// Record is one claimed, unpublished event. The relay never interprets Payload.
type Record struct {
	EventID      uuid.UUID
	Topic        string
	Key          string
	Payload      []byte
	AttemptCount int
	EnqueuedAt   time.Time
}

// Store is implemented once per feature, against that feature's own outbox table.
// Claim must return only rows whose business transaction has committed, selecting
// them under FOR UPDATE SKIP LOCKED, stamping a lease owned by owner, and returning
// that lease's expiry. An expired lease must be reclaimable by a different owner.
// MarkPublished must be called only after a broker acknowledgement.
//
// Reschedule must persist attemptCount as the row's TRANSPORT attempt counter and
// surface it again in Record.AttemptCount, which is what drives exponential backoff.
// It must not touch any DOMAIN attempt counter — a broker retry is not a processing
// attempt, and conflating the two burns a user's processing budget on a broker hiccup.
// Claimed is one batch plus the lease expiry the database stamped for it. Using
// that expiry prevents a slow Claim call from silently extending the lease.
type Claimed struct {
	Records        []Record
	LeaseExpiresAt time.Time
}

type Store interface {
	Claim(ctx context.Context, owner string, limit int) (Claimed, error)
	MarkPublished(ctx context.Context, eventID uuid.UUID, at time.Time) error
	Reschedule(ctx context.Context, eventID uuid.UUID, nextAttemptAt time.Time, attemptCount int, errorCode string) error
}

// Publisher returns only after the broker durably acknowledges the record.
// Implementations must use acks=all. Duplicate records are tolerated, so
// enabling Kafka's idempotent producer protocol is optional.
type Publisher interface {
	// Publish must not return while a broker write remains queued in the
	// background. In particular, cancellation must stop and join any in-flight
	// work before returning so the relay can safely release its lease.
	Publish(ctx context.Context, topic, key string, payload []byte) error
}

// RelayOptions configures one relay instance.
type RelayOptions struct {
	Owner       string
	BatchSize   int
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
	Now         func() time.Time
	Jitter      func(time.Duration) time.Duration
	LeaseMargin time.Duration
}

var (
	ErrOutboxNotUpdated   = errors.New("outbox row not updated after broker acknowledgement")
	ErrMissingLeaseExpiry = errors.New("claimed outbox batch has no lease expiry")
)

const (
	defaultBatchSize   = 50
	defaultBaseBackoff = 250 * time.Millisecond
	defaultMaxBackoff  = 30 * time.Second
)

func (options RelayOptions) withDefaults() RelayOptions {
	if options.BatchSize <= 0 {
		options.BatchSize = defaultBatchSize
	}
	if options.BaseBackoff <= 0 {
		options.BaseBackoff = defaultBaseBackoff
	}
	if options.MaxBackoff <= 0 {
		options.MaxBackoff = defaultMaxBackoff
	}
	if options.Now == nil {
		options.Now = func() time.Time { return time.Now().UTC() }
	}
	if options.Jitter == nil {
		options.Jitter = func(backoff time.Duration) time.Duration {
			return backoff/2 + time.Duration(rand.Int64N(int64(backoff/2)+1))
		}
	}
	return options
}

// Relay publishes committed outbox rows and marks them published only after the
// broker acknowledges them.
type Relay struct {
	store     Store
	publisher Publisher
	options   RelayOptions
}

func NewRelay(store Store, publisher Publisher, options RelayOptions) *Relay {
	return &Relay{store: store, publisher: publisher, options: options.withDefaults()}
}

// DrainOnce claims one batch and publishes it, returning the number of records
// the broker acknowledged.
func (relay *Relay) DrainOnce(ctx context.Context) (int, error) {
	claimed, err := relay.store.Claim(ctx, relay.options.Owner, relay.options.BatchSize)
	if err != nil {
		return 0, err
	}

	leaseExpiresAt := claimed.LeaseExpiresAt
	if len(claimed.Records) > 0 && leaseExpiresAt.IsZero() {
		return 0, ErrMissingLeaseExpiry
	}

	settled := 0
	var unsettled error
	for _, outboxRecord := range claimed.Records {
		if relay.leaseTooShort(leaseExpiresAt) {
			leaseExhaustedTotal.Add(1)
			break
		}

		publishErr := relay.publishWithinLease(ctx, outboxRecord, leaseExpiresAt)
		if publishErr != nil {
			publishFailureTotal.Add(1)
			nextAttempt := outboxRecord.AttemptCount + 1
			nextAttemptAt := relay.now().Add(relay.backoffFor(outboxRecord.AttemptCount))
			if rescheduleErr := relay.store.Reschedule(ctx, outboxRecord.EventID, nextAttemptAt, nextAttempt, transportErrorCode(publishErr)); rescheduleErr != nil {
				unsettled = errors.Join(unsettled, rescheduleErr)
			}
			continue
		}
		publishedTotal.Add(1)
		relay.recordDeliveryLag(outboxRecord)

		if markErr := relay.store.MarkPublished(ctx, outboxRecord.EventID, relay.now()); markErr != nil {
			outboxUpdateFailureTotal.Add(1)
			unsettled = errors.Join(unsettled, fmt.Errorf("%w: event %s reached the broker but its outbox row was not updated: %w", ErrOutboxNotUpdated, outboxRecord.EventID, markErr))
			continue
		}
		settled++
	}
	return settled, unsettled
}

// publishWithinLease bounds the write by the lease itself. A preflight check
// alone cannot stop a write that starts inside the lease and finishes after
// another relay has reclaimed the row.
func (relay *Relay) publishWithinLease(ctx context.Context, outboxRecord Record, leaseExpiresAt time.Time) error {
	if leaseExpiresAt.IsZero() {
		return relay.publisher.Publish(ctx, outboxRecord.Topic, outboxRecord.Key, outboxRecord.Payload)
	}

	// Reserve LeaseMargin for recording the acknowledgement. The publisher's
	// stricter contract guarantees no queued write survives this deadline.
	publishDeadline := leaseExpiresAt.Add(-relay.options.LeaseMargin)
	publishCtx, cancel := context.WithDeadline(ctx, publishDeadline)
	defer cancel()
	return relay.publisher.Publish(publishCtx, outboxRecord.Topic, outboxRecord.Key, outboxRecord.Payload)
}

// leaseTooShort stops a drain before the relay's own lease expires. Publishing
// under an expired lease races the relay that reclaimed the row, so the batch
// ends and the remaining rows stay claimable.
func (relay *Relay) leaseTooShort(leaseExpiresAt time.Time) bool {
	if leaseExpiresAt.IsZero() {
		return false
	}
	return !relay.now().Add(relay.options.LeaseMargin).Before(leaseExpiresAt)
}

func (relay *Relay) recordDeliveryLag(outboxRecord Record) {
	if outboxRecord.EnqueuedAt.IsZero() {
		return
	}
	lastDeliveryLagMillis.Store(relay.now().Sub(outboxRecord.EnqueuedAt).Milliseconds())
}

func (relay *Relay) backoffFor(attemptCount int) time.Duration {
	backoff := relay.options.BaseBackoff
	for attempt := 0; attempt < attemptCount && backoff < relay.options.MaxBackoff; attempt++ {
		backoff *= 2
	}
	if backoff > relay.options.MaxBackoff {
		backoff = relay.options.MaxBackoff
	}
	return relay.options.Jitter(backoff)
}

func (relay *Relay) now() time.Time {
	return relay.options.Now()
}

func transportErrorCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "PUBLISH_TIMEOUT"
	}
	return "PUBLISH_FAILED"
}
