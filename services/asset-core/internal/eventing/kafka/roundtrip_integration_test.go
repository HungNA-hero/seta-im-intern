package kafka

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/segmentio/kafka-go"

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

func (store *singleRecordStore) Claim(_ context.Context, _ string, _ int) (outbox.Claimed, error) {
	if store.published {
		return outbox.Claimed{}, nil
	}
	return outbox.Claimed{
		Records:        []outbox.Record{store.record},
		LeaseExpiresAt: time.Now().Add(5 * time.Minute),
	}, nil
}

func (store *singleRecordStore) MarkPublished(_ context.Context, _ uuid.UUID, _ time.Time) error {
	store.published = true
	return nil
}

func (store *singleRecordStore) Reschedule(_ context.Context, _ uuid.UUID, _ time.Time, _ int, _ string) error {
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

	producer := newTestProducer(t, brokers)
	defer producer.Close()

	store := &singleRecordStore{record: outbox.Record{EventID: eventID, Topic: topic, Key: jobID, Payload: payload}}
	relay := outbox.NewRelay(store, producer, outbox.RelayOptions{Owner: "relay-integration", BatchSize: 10})

	settled, err := relay.DrainOnce(ctx)
	if err != nil || settled != 1 {
		t.Fatalf("DrainOnce settled %d records, err = %v", settled, err)
	}

	effect := &collectingEffect{seen: make(chan event.Envelope, 4)}
	reader := newTestReader(t,
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
	producer := newTestProducer(t, brokers)
	defer producer.Close()

	poisonKey := uuid.NewString()
	if err := producer.Publish(ctx, topic, poisonKey, []byte(`{"schemaVersion":`)); err != nil {
		t.Fatalf("publishing poison: %v", err)
	}

	quarantine := &recordingQuarantine{isolated: make(chan consume.QuarantinedRecord, 8)}
	reader := newTestReader(t,
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

func newTestProducer(t *testing.T, brokers []string) *Producer {
	t.Helper()
	producer, err := NewProducer(ProducerOptions{Brokers: brokers})
	if err != nil {
		t.Fatalf("building producer: %v", err)
	}
	return producer
}

func newTestReader(t *testing.T, consumer *consume.Consumer, options ReaderOptions) *Reader {
	t.Helper()
	reader, err := NewReader(consumer, options)
	if err != nil {
		t.Fatalf("building reader: %v", err)
	}
	return reader
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

type flakyEffect struct {
	failuresLeft int
	applied      chan string
}

func (effect *flakyEffect) Apply(_ context.Context, envelope event.Envelope) error {
	if effect.failuresLeft > 0 {
		effect.failuresLeft--
		return errors.New("database unavailable")
	}
	effect.applied <- envelope.EventID
	return nil
}

func TestATransientlyFailingRecordIsNotSkippedByLaterRecords(t *testing.T) {
	brokers := brokersOrSkip(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	topic := "kan81-inorder-" + uuid.NewString()
	createTopic(t, brokers, topic)

	producer := newTestProducer(t, brokers)
	defer producer.Close()

	key := uuid.NewString()
	first, second := uuid.New(), uuid.New()
	for _, eventID := range []uuid.UUID{first, second} {
		publishWhenTopicIsReady(t, ctx, producer, topic, key, envelopeFor(eventID, key))
	}

	effect := &flakyEffect{failuresLeft: 2, applied: make(chan string, 4)}
	reader := newTestReader(t,
		consume.NewConsumer(effect, &recordingQuarantine{isolated: make(chan consume.QuarantinedRecord, 4)}, consume.ConsumerOptions{
			MaxValueBytes: 2048,
			KnownVersions: []int{1},
		}),
		ReaderOptions{
			Brokers:             brokers,
			Topic:               topic,
			GroupID:             "kan81-inorder-" + uuid.NewString(),
			MaxDeliveryAttempts: 5,
			RetryBackoff:        200 * time.Millisecond,
		},
	)
	defer reader.Close()
	go func() { _ = reader.Run(ctx) }()

	var order []string
	for len(order) < 2 {
		select {
		case eventID := <-effect.applied:
			order = append(order, eventID)
		case <-ctx.Done():
			t.Fatalf("only %d of 2 records were applied: %v", len(order), order)
		}
	}

	if order[0] != first.String() {
		t.Fatalf("applied order = %v, want the transiently failing record %s applied first, never skipped", order, first)
	}
	if order[1] != second.String() {
		t.Fatalf("applied order = %v, want %s second", order, second)
	}
}

// publishWhenTopicIsReady absorbs the window between CreateTopics returning and
// the producer's metadata learning the topic exists.
func publishWhenTopicIsReady(t *testing.T, ctx context.Context, producer *Producer, topic, key string, payload []byte) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := producer.Publish(ctx, topic, key, payload)
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("publishing to %s: %v", topic, err)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func envelopeFor(eventID uuid.UUID, jobID string) []byte {
	return []byte(`{"eventId":"` + eventID.String() + `","eventType":"media.processing.requested",` +
		`"schemaVersion":1,"source":"asset-core","occurredAt":"2026-08-12T10:00:00Z",` +
		`"orgId":"10000000-0000-0000-0000-000000000001","jobId":"` + jobID + `"}`)
}

func createTopic(t *testing.T, brokers []string, topic string) {
	t.Helper()
	conn, err := kafka.Dial("tcp", brokers[0])
	if err != nil {
		t.Fatalf("dialing broker: %v", err)
	}
	defer conn.Close()
	if err := conn.CreateTopics(kafka.TopicConfig{Topic: topic, NumPartitions: 1, ReplicationFactor: 1}); err != nil {
		t.Fatalf("creating topic %s: %v", topic, err)
	}

	// CreateTopics returns before the topic is visible in cluster metadata, so
	// publishing immediately races it with UNKNOWN_TOPIC_OR_PARTITION.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		partitions, err := conn.ReadPartitions(topic)
		if err == nil && len(partitions) > 0 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("topic %s never became visible in cluster metadata", topic)
}
