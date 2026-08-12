package kafka

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"seta-im-intern/go-asset-core/internal/eventing/consume"
	"seta-im-intern/go-asset-core/internal/eventing/event"
	"seta-im-intern/go-asset-core/internal/eventing/outbox"
)

// These tests need a live broker and are skipped unless ASSET_KAFKA_BROKERS is
// set, so the default `go test ./...` stays network-free.
func brokersOrSkip(t *testing.T) []string {
	t.Helper()
	brokers := strings.TrimSpace(os.Getenv("ASSET_KAFKA_BROKERS"))
	if brokers == "" {
		t.Skip("set ASSET_KAFKA_BROKERS to run Kafka round-trip tests")
	}
	return strings.Split(brokers, ",")
}

type singleRecordStore struct {
	record    outbox.Record
	published bool
}

func (store *singleRecordStore) Claim(_ context.Context, _ string, _ int) ([]outbox.Record, error) {
	if store.published {
		return nil, nil
	}
	return []outbox.Record{store.record}, nil
}

func (store *singleRecordStore) MarkPublished(_ context.Context, _ uuid.UUID, _ time.Time) error {
	store.published = true
	return nil
}

func (store *singleRecordStore) Reschedule(_ context.Context, _ uuid.UUID, _ time.Time, _ string) error {
	return nil
}

type collectingEffect struct {
	seen chan event.Envelope
}

func (effect *collectingEffect) Apply(_ context.Context, envelope event.Envelope) error {
	effect.seen <- envelope
	return nil
}

type recordingQuarantine struct {
	isolated chan consume.QuarantinedRecord
}

func (quarantine *recordingQuarantine) Isolate(_ context.Context, record consume.QuarantinedRecord) error {
	quarantine.isolated <- record
	return nil
}

func TestRelayPublishesAndTheConsumerAppliesTheSameEvent(t *testing.T) {
	brokers := brokersOrSkip(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	topic := envOr("ASSET_KAFKA_MEDIA_TOPIC", "media-processing.v1")
	eventID := uuid.New()
	jobID := uuid.New().String()
	payload := []byte(`{"eventId":"` + eventID.String() + `","eventType":"media.processing.requested",` +
		`"schemaVersion":1,"source":"asset-core","occurredAt":"2026-08-12T10:00:00Z",` +
		`"orgId":"10000000-0000-0000-0000-000000000001","jobId":"` + jobID + `"}`)

	producer := NewProducer(ProducerOptions{Brokers: brokers})
	defer producer.Close()

	store := &singleRecordStore{record: outbox.Record{EventID: eventID, Topic: topic, Key: jobID, Payload: payload}}
	relay := outbox.NewRelay(store, producer, outbox.RelayOptions{Owner: "relay-integration", BatchSize: 10})

	settled, err := relay.DrainOnce(ctx)
	if err != nil || settled != 1 {
		t.Fatalf("DrainOnce settled %d records, err = %v", settled, err)
	}

	effect := &collectingEffect{seen: make(chan event.Envelope, 4)}
	reader := NewReader(
		consume.NewConsumer(effect, &recordingQuarantine{isolated: make(chan consume.QuarantinedRecord, 4)}, consume.ConsumerOptions{
			MaxValueBytes: 2048,
			KnownVersions: []int{1},
		}),
		ReaderOptions{Brokers: brokers, Topic: topic, GroupID: "media-workers-roundtrip-" + uuid.NewString()},
	)
	defer reader.Close()
	go func() { _ = reader.Run(ctx) }()

	for {
		select {
		case envelope := <-effect.seen:
			if envelope.EventID == eventID.String() {
				return
			}
		case <-ctx.Done():
			t.Fatal("the published event never reached the consumer")
		}
	}
}

func TestConsumerQuarantinesPoisonFromARealTopic(t *testing.T) {
	brokers := brokersOrSkip(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	topic := envOr("ASSET_KAFKA_MEDIA_TOPIC", "media-processing.v1")
	producer := NewProducer(ProducerOptions{Brokers: brokers})
	defer producer.Close()

	poisonKey := uuid.NewString()
	if err := producer.Publish(ctx, topic, poisonKey, []byte(`{"schemaVersion":`)); err != nil {
		t.Fatalf("publishing poison: %v", err)
	}

	quarantine := &recordingQuarantine{isolated: make(chan consume.QuarantinedRecord, 8)}
	reader := NewReader(
		consume.NewConsumer(&collectingEffect{seen: make(chan event.Envelope, 8)}, quarantine, consume.ConsumerOptions{
			MaxValueBytes: 2048,
			KnownVersions: []int{1},
		}),
		ReaderOptions{Brokers: brokers, Topic: topic, GroupID: "media-workers-poison-" + uuid.NewString()},
	)
	defer reader.Close()
	go func() { _ = reader.Run(ctx) }()

	for {
		select {
		case isolated := <-quarantine.isolated:
			if isolated.ReasonCode != "MALFORMED_ENVELOPE" {
				continue
			}
			if len(isolated.QuarantineID) != 64 {
				t.Fatalf("quarantineId = %q, want 64 hex characters", isolated.QuarantineID)
			}
			return
		case <-ctx.Done():
			t.Fatal("poison record was never quarantined")
		}
	}
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
