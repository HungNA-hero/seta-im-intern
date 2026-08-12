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
}

// Store is implemented once per feature, against that feature's own outbox table.
// Claim must return only rows whose business transaction has committed, selecting
// them under FOR UPDATE SKIP LOCKED and stamping a lease owned by owner. An expired
// lease must be reclaimable by a different owner. MarkPublished must be called only
// after a broker acknowledgement. Reschedule must not alter any domain-level attempt
// counter — transport retries are not processing attempts.
type Store interface {
	Claim(ctx context.Context, owner string, limit int) ([]Record, error)
	MarkPublished(ctx context.Context, eventID uuid.UUID, at time.Time) error
	Reschedule(ctx context.Context, eventID uuid.UUID, nextAttemptAt time.Time, errorCode string) error
}

// Publisher returns only after the broker durably acknowledges the record.
// Implementations must enable idempotent production and acks=all.
type Publisher interface {
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
}

var ErrOutboxNotUpdated = errors.New("outbox row not updated after broker acknowledgement")

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
	records, err := relay.store.Claim(ctx, relay.options.Owner, relay.options.BatchSize)
	if err != nil {
		return 0, err
	}

	settled := 0
	var unsettled error
	for _, outboxRecord := range records {
		publishErr := relay.publisher.Publish(ctx, outboxRecord.Topic, outboxRecord.Key, outboxRecord.Payload)
		if publishErr != nil {
			nextAttemptAt := relay.now().Add(relay.backoffFor(outboxRecord.AttemptCount))
			if rescheduleErr := relay.store.Reschedule(ctx, outboxRecord.EventID, nextAttemptAt, transportErrorCode(publishErr)); rescheduleErr != nil {
				unsettled = errors.Join(unsettled, rescheduleErr)
			}
			continue
		}

		if markErr := relay.store.MarkPublished(ctx, outboxRecord.EventID, relay.now()); markErr != nil {
			unsettled = errors.Join(unsettled, fmt.Errorf("%w: event %s reached the broker but its outbox row was not updated: %w", ErrOutboxNotUpdated, outboxRecord.EventID, markErr))
			continue
		}
		settled++
	}
	return settled, unsettled
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
