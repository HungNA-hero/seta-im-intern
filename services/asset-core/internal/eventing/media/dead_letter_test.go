package media

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"seta-im-intern/go-asset-core/internal/eventing/consume"
)

type recordingIsolationStore struct {
	calls   *[]string
	records []consume.QuarantinedRecord
	err     error
}

func (store *recordingIsolationStore) IsolateNotification(_ context.Context, record consume.QuarantinedRecord) error {
	*store.calls = append(*store.calls, "database")
	store.records = append(store.records, record)
	return store.err
}

type recordingDeadLetterPublisher struct {
	calls   *[]string
	topic   string
	key     string
	payload []byte
	err     error
}

func (publisher *recordingDeadLetterPublisher) Publish(_ context.Context, topic, key string, payload []byte) error {
	*publisher.calls = append(*publisher.calls, "broker")
	publisher.topic = topic
	publisher.key = key
	publisher.payload = append([]byte(nil), payload...)
	return publisher.err
}

func TestDeadLetterIsolatesIdentifiableJobBeforePublishingSanitizedRecord(t *testing.T) {
	calls := []string{}
	store := &recordingIsolationStore{calls: &calls}
	publisher := &recordingDeadLetterPublisher{calls: &calls}
	deadLetter := NewDeadLetter(store, publisher, DeadLetterTopic)
	record := consume.QuarantinedRecord{
		SourceTopic:     "media-processing.v1",
		SourcePartition: 3,
		SourceOffset:    42,
		QuarantineID:    "64f4f7570b3f7b1ec67f1ea7a80ff2ec9f44acb91544a456b820087aa62ed273",
		EventID:         uuid.NewString(),
		AggregateID:     uuid.NewString(),
		ObservedAt:      time.Date(2026, time.August, 19, 10, 1, 0, 0, time.FixedZone("ICT", 7*60*60)),
		ReasonCode:      "UNSUPPORTED_SCHEMA_VERSION",
		PayloadSHA256:   "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	}

	if err := deadLetter.Isolate(context.Background(), record); err != nil {
		t.Fatalf("Isolate: %v", err)
	}

	if len(calls) != 2 || calls[0] != "database" || calls[1] != "broker" {
		t.Fatalf("calls = %v, want database isolation before broker publication", calls)
	}
	if publisher.topic != DeadLetterTopic || publisher.key != record.QuarantineID {
		t.Fatalf("published to %q with key %q, want %q and deterministic key %q", publisher.topic, publisher.key, DeadLetterTopic, record.QuarantineID)
	}
	if len(publisher.payload) > MaxDeadLetterBytes {
		t.Fatalf("dead-letter payload = %d bytes, want at most %d", len(publisher.payload), MaxDeadLetterBytes)
	}

	var wire map[string]any
	if err := json.Unmarshal(publisher.payload, &wire); err != nil {
		t.Fatalf("decode published dead letter: %v", err)
	}
	if wire["jobId"] != record.AggregateID || wire["eventId"] != record.EventID {
		t.Fatalf("published identities = eventId %v, jobId %v; want validated correlation identities", wire["eventId"], wire["jobId"])
	}
	if wire["observedAt"] != "2026-08-19T03:01:00Z" {
		t.Fatalf("observedAt = %v, want normalized UTC", wire["observedAt"])
	}
	for _, prohibited := range []string{"payload", "filename", "objectKey", "url", "stack"} {
		if _, present := wire[prohibited]; present {
			t.Fatalf("dead-letter record exposed prohibited field %q", prohibited)
		}
	}
}

func TestDeadLetterBrokerFailureOccursAfterDurableIsolationAndIsReturned(t *testing.T) {
	calls := []string{}
	store := &recordingIsolationStore{calls: &calls}
	publisher := &recordingDeadLetterPublisher{calls: &calls, err: errors.New("broker unavailable")}
	deadLetter := NewDeadLetter(store, publisher, DeadLetterTopic)

	err := deadLetter.Isolate(context.Background(), consume.QuarantinedRecord{
		SourceTopic:     "media-processing.v1",
		SourcePartition: 0,
		SourceOffset:    42,
		QuarantineID:    "64f4f7570b3f7b1ec67f1ea7a80ff2ec9f44acb91544a456b820087aa62ed273",
		AggregateID:     uuid.NewString(),
		ObservedAt:      time.Now(),
		ReasonCode:      "MALFORMED_ENVELOPE",
		PayloadSHA256:   "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	})

	if err == nil || !errors.Is(err, publisher.err) {
		t.Fatalf("Isolate error = %v, want broker failure returned to keep the source offset uncommitted", err)
	}
	if len(store.records) != 1 || len(calls) != 2 || calls[0] != "database" || calls[1] != "broker" {
		t.Fatalf("records = %d, calls = %v; durable isolation must precede the failed publish", len(store.records), calls)
	}
}

func TestDeadLetterOmitsUnvalidatedOptionalIdentities(t *testing.T) {
	calls := []string{}
	store := &recordingIsolationStore{calls: &calls}
	publisher := &recordingDeadLetterPublisher{calls: &calls}
	deadLetter := NewDeadLetter(store, publisher, DeadLetterTopic)

	if err := deadLetter.Isolate(context.Background(), consume.QuarantinedRecord{
		SourceTopic:     "media-processing.v1",
		SourcePartition: 0,
		SourceOffset:    42,
		QuarantineID:    "64f4f7570b3f7b1ec67f1ea7a80ff2ec9f44acb91544a456b820087aa62ed273",
		EventID:         "secret-filename.jpg",
		AggregateID:     "https://storage.invalid/object?signature=secret",
		ObservedAt:      time.Now(),
		ReasonCode:      "INVALID_ENVELOPE_FIELD",
		PayloadSHA256:   "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	}); err != nil {
		t.Fatalf("Isolate: %v", err)
	}
	if len(store.records) != 0 {
		t.Fatalf("invalid job identity reached database isolation: %#v", store.records)
	}

	var wire map[string]any
	if err := json.Unmarshal(publisher.payload, &wire); err != nil {
		t.Fatalf("decode dead letter: %v", err)
	}
	if _, present := wire["eventId"]; present {
		t.Fatalf("unvalidated event identity leaked: %v", wire["eventId"])
	}
	if _, present := wire["jobId"]; present {
		t.Fatalf("unvalidated job identity leaked: %v", wire["jobId"])
	}
}

func TestDeadLetterPublishesToTheConfiguredTopic(t *testing.T) {
	calls := []string{}
	store := &recordingIsolationStore{calls: &calls}
	publisher := &recordingDeadLetterPublisher{calls: &calls}
	deadLetter := NewDeadLetter(store, publisher, "tenant-media-dlq.v2")

	if err := deadLetter.Isolate(context.Background(), consume.QuarantinedRecord{
		SourceTopic:     "media-processing.v1",
		SourcePartition: 0,
		SourceOffset:    7,
		QuarantineID:    "64f4f7570b3f7b1ec67f1ea7a80ff2ec9f44acb91544a456b820087aa62ed273",
		ObservedAt:      time.Now(),
		ReasonCode:      "MALFORMED_ENVELOPE",
		PayloadSHA256:   "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	}); err != nil {
		t.Fatalf("Isolate: %v", err)
	}
	if publisher.topic != "tenant-media-dlq.v2" {
		t.Fatalf("published topic = %q, want configured topic", publisher.topic)
	}
}
