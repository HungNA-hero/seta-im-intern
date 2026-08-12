package consume

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"seta-im-intern/go-asset-core/internal/eventing/event"
)

type Record struct {
	Topic     string
	Partition int
	Offset    int64
	Key       string
	Value     []byte
}

type Outcome int

const (
	LeaveUncommitted Outcome = iota
	CommitOffset
)

func (outcome Outcome) String() string {
	if outcome == CommitOffset {
		return "CommitOffset"
	}
	return "LeaveUncommitted"
}

type Effect interface {
	Apply(ctx context.Context, envelope event.Envelope) error
}

type Quarantine interface {
	Isolate(ctx context.Context, quarantined QuarantinedRecord) error
}

var ErrAlreadyApplied = errors.New("event already applied")

type ConsumerOptions struct {
	MaxValueBytes  int
	KnownVersions  []int
	RoutableTypes  []string
	AggregateField string
	Now            func() time.Time
}

type Consumer struct {
	effect     Effect
	quarantine Quarantine
	options    ConsumerOptions
}

func NewConsumer(effect Effect, quarantine Quarantine, options ConsumerOptions) *Consumer {
	return &Consumer{effect: effect, quarantine: quarantine, options: options}
}

func (consumer *Consumer) Deliver(ctx context.Context, record Record) (Outcome, error) {
	if reason, poisoned := consumer.classify(record); poisoned {
		return consumer.isolate(ctx, record, reason)
	}

	envelope, err := event.Parse(record.Value, consumer.options.KnownVersions)
	if err != nil {
		return consumer.isolate(ctx, record, reasonFor(err))
	}

	if err := consumer.effect.Apply(ctx, envelope); err != nil {
		if errors.Is(err, ErrAlreadyApplied) {
			return CommitOffset, nil
		}
		return LeaveUncommitted, err
	}
	return CommitOffset, nil
}

func (consumer *Consumer) classify(record Record) (string, bool) {
	if consumer.options.MaxValueBytes > 0 && len(record.Value) > consumer.options.MaxValueBytes {
		return "OVERSIZED_RECORD", true
	}
	return "", false
}

func reasonFor(err error) string {
	switch {
	case errors.Is(err, event.ErrUnknownVersion):
		return "UNSUPPORTED_SCHEMA_VERSION"
	case errors.Is(err, event.ErrMissingField):
		return "INCOMPLETE_ENVELOPE"
	default:
		return "MALFORMED_ENVELOPE"
	}
}

func (consumer *Consumer) isolate(ctx context.Context, record Record, reason string) (Outcome, error) {
	quarantined := QuarantinedRecord{
		SourceTopic:     record.Topic,
		SourcePartition: record.Partition,
		SourceOffset:    record.Offset,
		QuarantineID:    quarantineID(record, reason),
		ObservedAt:      consumer.now(),
		ReasonCode:      reason,
		PayloadSHA256:   digest(record.Value),
	}

	if err := consumer.quarantine.Isolate(ctx, quarantined); err != nil {
		return LeaveUncommitted, fmt.Errorf("isolating %s at %s/%d/%d: %w", reason, record.Topic, record.Partition, record.Offset, err)
	}
	return CommitOffset, nil
}

func quarantineID(record Record, reason string) string {
	return digest([]byte(fmt.Sprintf("%s\x00%d\x00%d\x00%s", record.Topic, record.Partition, record.Offset, reason)))
}

func digest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func (consumer *Consumer) now() time.Time {
	if consumer.options.Now != nil {
		return consumer.options.Now()
	}
	return time.Now().UTC()
}
