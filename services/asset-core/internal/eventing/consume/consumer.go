package consume

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"seta-im-intern/go-asset-core/internal/eventing/event"
	"seta-im-intern/go-asset-core/internal/requestcontext"
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

// ConsumerOptions declares what this consumer accepts. RoutableTypes and
// AggregateField are per-family declarations: an event type outside
// RoutableTypes, or a record key disagreeing with the envelope's
// AggregateField, is poison rather than something to apply.
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
	if consumer.options.MaxValueBytes > 0 && len(record.Value) > consumer.options.MaxValueBytes {
		return consumer.isolate(ctx, record, "OVERSIZED_RECORD", event.Envelope{})
	}

	envelope, err := event.Parse(record.Value, consumer.options.KnownVersions)
	if err != nil {
		return consumer.isolate(ctx, record, reasonFor(err), consumer.salvageIdentity(record))
	}
	if reason, poisoned := consumer.classify(record, envelope); poisoned {
		return consumer.isolate(ctx, record, reason, envelope)
	}

	if err := consumer.effect.Apply(correlated(ctx, envelope), envelope); err != nil {
		if errors.Is(err, ErrAlreadyApplied) {
			duplicateTotal.Add(1)
			return CommitOffset, nil
		}
		transientFailureTotal.Add(1)
		return LeaveUncommitted, err
	}
	appliedTotal.Add(1)
	return CommitOffset, nil
}

func (consumer *Consumer) classify(record Record, envelope event.Envelope) (string, bool) {
	if !consumer.routable(envelope.EventType) {
		return "UNROUTABLE_EVENT_TYPE", true
	}
	if consumer.options.AggregateField == "" {
		return "", false
	}

	aggregateID, ok := consumer.aggregateID(envelope)
	if !ok {
		return "MISSING_AGGREGATE_ID", true
	}
	if record.Key != aggregateID {
		return "KEY_AGGREGATE_MISMATCH", true
	}
	return "", false
}

func (consumer *Consumer) routable(eventType string) bool {
	if len(consumer.options.RoutableTypes) == 0 {
		return true
	}
	for _, routable := range consumer.options.RoutableTypes {
		if routable == eventType {
			return true
		}
	}
	return false
}

// aggregateID reports the declared aggregate identifier and whether it is a
// usable UUID. A configured aggregate field that is absent, non-string, or not a
// UUID is deterministic poison, not something to pass through unchecked.
func (consumer *Consumer) aggregateID(envelope event.Envelope) (string, bool) {
	if consumer.options.AggregateField == "" || len(envelope.Raw) == 0 {
		return "", false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Raw, &fields); err != nil {
		return "", false
	}
	raw, present := fields[consumer.options.AggregateField]
	if !present {
		return "", false
	}
	var aggregateID string
	if err := json.Unmarshal(raw, &aggregateID); err != nil {
		return "", false
	}
	if !isUUID(aggregateID) {
		return "", false
	}
	return aggregateID, true
}

// isUUID gates every identifier copied into a dead-letter record. Without it a
// hostile producer could place arbitrary payload text into fields the
// dead-letter contract requires to be sanitized and bounded.
func isUUID(value string) bool {
	_, err := uuid.Parse(value)
	return err == nil
}

// salvageIdentity recovers only the identifiers an operator needs to correlate
// an unparseable record, and only when each is individually a valid UUID. Any
// other value is attacker-controlled payload text and is dropped.
func (consumer *Consumer) salvageIdentity(record Record) event.Envelope {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(record.Value, &fields); err != nil {
		return event.Envelope{}
	}

	identity := event.Envelope{Raw: record.Value}
	rawEventID, present := fields["eventId"]
	if !present {
		return identity
	}
	var eventID string
	if err := json.Unmarshal(rawEventID, &eventID); err == nil && isUUID(eventID) {
		identity.EventID = eventID
	}
	return identity
}

func reasonFor(err error) string {
	switch {
	case errors.Is(err, event.ErrUnknownVersion):
		return "UNSUPPORTED_SCHEMA_VERSION"
	case errors.Is(err, event.ErrMissingField):
		return "INCOMPLETE_ENVELOPE"
	case errors.Is(err, event.ErrInvalidField):
		return "INVALID_ENVELOPE_FIELD"
	default:
		return "MALFORMED_ENVELOPE"
	}
}

func (consumer *Consumer) isolate(ctx context.Context, record Record, reason string, identity event.Envelope) (Outcome, error) {
	aggregateID, _ := consumer.aggregateID(identity)

	quarantined := QuarantinedRecord{
		SourceTopic:     record.Topic,
		SourcePartition: record.Partition,
		SourceOffset:    record.Offset,
		QuarantineID:    quarantineID(record, reason),
		EventID:         identity.EventID,
		AggregateID:     aggregateID,
		ObservedAt:      consumer.now(),
		ReasonCode:      reason,
		PayloadSHA256:   digest(record.Value),
	}

	if err := consumer.quarantine.Isolate(ctx, quarantined); err != nil {
		transientFailureTotal.Add(1)
		return LeaveUncommitted, fmt.Errorf("isolating %s at %s/%d/%d: %w", reason, record.Topic, record.Partition, record.Offset, err)
	}
	quarantinedTotal.Add(1)
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

func correlated(ctx context.Context, envelope event.Envelope) context.Context {
	traceID := event.ParseTraceID(envelope.Traceparent)
	if traceID == "" {
		return ctx
	}
	return requestcontext.WithCorrelation(ctx, &requestcontext.Correlation{
		TraceID:   traceID,
		RequestID: envelope.EventID,
		StartedAt: time.Now().UTC(),
	})
}
