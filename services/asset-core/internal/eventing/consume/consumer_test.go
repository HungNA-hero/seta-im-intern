package consume

import (
	"context"
	"errors"
	"strings"
	"testing"

	"seta-im-intern/go-asset-core/internal/eventing/event"
	"seta-im-intern/go-asset-core/internal/requestcontext"
)

type fakeEffect struct {
	applied []string
	err     error
	calls   int
}

func (fake *fakeEffect) Apply(_ context.Context, envelope event.Envelope) error {
	fake.calls++
	if fake.err != nil {
		return fake.err
	}
	fake.applied = append(fake.applied, envelope.EventID)
	return nil
}

type fakeQuarantine struct {
	isolated []QuarantinedRecord
	err      error
}

func (fake *fakeQuarantine) Isolate(_ context.Context, quarantined QuarantinedRecord) error {
	fake.isolated = append(fake.isolated, quarantined)
	return fake.err
}

const validEnvelope = `{"eventId":"6e2f14ea-d7c7-4f1c-8f17-274c51d9bcb9","eventType":"media.processing.requested",` +
	`"schemaVersion":1,"source":"asset-core","occurredAt":"2026-08-06T10:00:00Z",` +
	`"orgId":"10000000-0000-0000-0000-000000000001","jobId":"a74e1124-b5c0-47b4-b73f-4ce7c7031d77"}`

func testRecord(value string) Record {
	return Record{
		Topic:     "media-processing.v1",
		Partition: 0,
		Offset:    42,
		Key:       "a74e1124-b5c0-47b4-b73f-4ce7c7031d77",
		Value:     []byte(value),
	}
}

func testConsumer(effect Effect, quarantine Quarantine) *Consumer {
	return NewConsumer(effect, quarantine, ConsumerOptions{
		MaxValueBytes:  2048,
		KnownVersions:  []int{1},
		RoutableTypes:  []string{"media.processing.requested"},
		AggregateField: "jobId",
	})
}

func TestConsumerCommitsWithoutASecondEffectWhenTheEventWasAlreadyApplied(t *testing.T) {
	effect := &fakeEffect{err: ErrAlreadyApplied}
	quarantine := &fakeQuarantine{}
	consumer := testConsumer(effect, quarantine)

	outcome, err := consumer.Deliver(context.Background(), testRecord(validEnvelope))

	if err != nil {
		t.Fatalf("a duplicate delivery is not an error: %v", err)
	}
	if outcome != CommitOffset {
		t.Fatalf("outcome = %v, want CommitOffset so a duplicate does not stall the partition", outcome)
	}
	if len(quarantine.isolated) != 0 {
		t.Fatalf("a valid duplicate was quarantined: %v", quarantine.isolated)
	}
	if effect.calls != 1 {
		t.Fatalf("effect consulted %d times, want 1 — idempotency is the effect's durable decision, not consumer memory", effect.calls)
	}
}

func TestConsumerLeavesTheOffsetUncommittedWhenTheEffectFailsTransiently(t *testing.T) {
	effect := &fakeEffect{err: errors.New("database unavailable")}
	quarantine := &fakeQuarantine{}
	consumer := testConsumer(effect, quarantine)

	outcome, err := consumer.Deliver(context.Background(), testRecord(validEnvelope))

	if err == nil {
		t.Fatal("a transient effect failure returned no error")
	}
	if outcome != LeaveUncommitted {
		t.Fatalf("outcome = %v, want LeaveUncommitted so the record is redelivered", outcome)
	}
	if len(quarantine.isolated) != 0 {
		t.Fatalf("a transient failure was quarantined as poison: %v", quarantine.isolated)
	}
}

func TestConsumerQuarantinesMalformedRecordsThenCommitsTheSourceOffset(t *testing.T) {
	effect := &fakeEffect{}
	quarantine := &fakeQuarantine{}
	consumer := testConsumer(effect, quarantine)

	outcome, err := consumer.Deliver(context.Background(), testRecord(`{"eventId": not json`))

	if err != nil {
		t.Fatalf("quarantining poison is a handled outcome, not an error: %v", err)
	}
	if outcome != CommitOffset {
		t.Fatalf("outcome = %v, want CommitOffset — retrying poison can never change the result", outcome)
	}
	if effect.calls != 0 {
		t.Fatalf("effect was applied %d times for an unparseable record, want 0", effect.calls)
	}
	if len(quarantine.isolated) != 1 {
		t.Fatalf("isolated %d records, want 1", len(quarantine.isolated))
	}

	isolated := quarantine.isolated[0]
	if isolated.ReasonCode != "MALFORMED_ENVELOPE" {
		t.Fatalf("reason code = %q, want MALFORMED_ENVELOPE", isolated.ReasonCode)
	}
	if isolated.SourceTopic != "media-processing.v1" || isolated.SourcePartition != 0 || isolated.SourceOffset != 42 {
		t.Fatalf("source diagnostics = %s/%d/%d, want media-processing.v1/0/42", isolated.SourceTopic, isolated.SourcePartition, isolated.SourceOffset)
	}
	if len(isolated.QuarantineID) != 64 || len(isolated.PayloadSHA256) != 64 {
		t.Fatalf("quarantineId=%q payloadSha256=%q, want 64 lowercase hex characters each", isolated.QuarantineID, isolated.PayloadSHA256)
	}
	if isolated.ObservedAt.IsZero() {
		t.Fatal("observedAt was not stamped")
	}
}

func TestConsumerQuarantineDiagnosticsCarryNoPayloadContent(t *testing.T) {
	effect := &fakeEffect{}
	quarantine := &fakeQuarantine{}
	consumer := testConsumer(effect, quarantine)
	poison := `{"schemaVersion":1,"objectKey":"org/secret-holiday-photo.jpg","token":"Bearer super-secret"`

	if _, err := consumer.Deliver(context.Background(), testRecord(poison)); err != nil {
		t.Fatalf("Deliver returned error: %v", err)
	}

	isolated := quarantine.isolated[0]
	for field, value := range map[string]string{
		"quarantineId":  isolated.QuarantineID,
		"payloadSha256": isolated.PayloadSHA256,
		"eventId":       isolated.EventID,
		"aggregateId":   isolated.AggregateID,
		"reasonCode":    isolated.ReasonCode,
	} {
		for _, secret := range []string{"secret-holiday-photo", "super-secret", "Bearer", "objectKey"} {
			if strings.Contains(value, secret) {
				t.Fatalf("quarantine field %s leaked payload content %q: %q", field, secret, value)
			}
		}
	}
}

func TestConsumerQuarantineIDIsDeterministicPerSourcePosition(t *testing.T) {
	first := &fakeQuarantine{}
	if _, err := testConsumer(&fakeEffect{}, first).Deliver(context.Background(), testRecord(`bad`)); err != nil {
		t.Fatalf("Deliver returned error: %v", err)
	}
	second := &fakeQuarantine{}
	if _, err := testConsumer(&fakeEffect{}, second).Deliver(context.Background(), testRecord(`bad`)); err != nil {
		t.Fatalf("Deliver returned error: %v", err)
	}
	if first.isolated[0].QuarantineID != second.isolated[0].QuarantineID {
		t.Fatal("the same source position produced different quarantine IDs, so duplicates cannot be collapsed")
	}

	elsewhere := &fakeQuarantine{}
	otherOffset := testRecord(`bad`)
	otherOffset.Offset = 43
	if _, err := testConsumer(&fakeEffect{}, elsewhere).Deliver(context.Background(), otherOffset); err != nil {
		t.Fatalf("Deliver returned error: %v", err)
	}
	if elsewhere.isolated[0].QuarantineID == first.isolated[0].QuarantineID {
		t.Fatal("different source offsets collapsed to one quarantine ID")
	}
}

func TestConsumerRejectsAnOversizedRecordWithoutParsingIt(t *testing.T) {
	effect := &fakeEffect{}
	quarantine := &fakeQuarantine{}
	consumer := testConsumer(effect, quarantine)
	oversized := testRecord(validEnvelope)
	oversized.Value = append(oversized.Value, make([]byte, 4096)...)

	outcome, err := consumer.Deliver(context.Background(), oversized)

	if err != nil || outcome != CommitOffset {
		t.Fatalf("outcome = %v, err = %v, want CommitOffset with no error", outcome, err)
	}
	if effect.calls != 0 {
		t.Fatal("an oversized record reached the effect")
	}
	if len(quarantine.isolated) != 1 || quarantine.isolated[0].ReasonCode != "OVERSIZED_RECORD" {
		t.Fatalf("isolated = %v, want one OVERSIZED_RECORD", quarantine.isolated)
	}
}

func TestConsumerQuarantinesAnUnknownSchemaVersion(t *testing.T) {
	quarantine := &fakeQuarantine{}
	consumer := testConsumer(&fakeEffect{}, quarantine)
	future := testRecord(strings.Replace(validEnvelope, `"schemaVersion":1`, `"schemaVersion":9`, 1))

	if _, err := consumer.Deliver(context.Background(), future); err != nil {
		t.Fatalf("Deliver returned error: %v", err)
	}
	if len(quarantine.isolated) != 1 || quarantine.isolated[0].ReasonCode != "UNSUPPORTED_SCHEMA_VERSION" {
		t.Fatalf("isolated = %v, want one UNSUPPORTED_SCHEMA_VERSION", quarantine.isolated)
	}
}

func TestConsumerDoesNotCommitWhenDatabaseFirstIsolationFails(t *testing.T) {
	quarantine := &fakeQuarantine{err: errors.New("database unavailable")}
	consumer := testConsumer(&fakeEffect{}, quarantine)

	outcome, err := consumer.Deliver(context.Background(), testRecord(`bad`))

	if err == nil {
		t.Fatal("a failed isolation returned no error")
	}
	if outcome != LeaveUncommitted {
		t.Fatalf("outcome = %v, want LeaveUncommitted — the offset must not advance past an unisolated poison record", outcome)
	}
}

func TestARestartedConsumerCarriesNoInMemoryDeduplicationState(t *testing.T) {
	effect := &fakeEffect{}
	quarantine := &fakeQuarantine{}

	outcome, err := testConsumer(effect, quarantine).Deliver(context.Background(), testRecord(validEnvelope))
	if err != nil || outcome != CommitOffset {
		t.Fatalf("first delivery: outcome = %v, err = %v", outcome, err)
	}

	effect.err = ErrAlreadyApplied
	restarted := testConsumer(effect, quarantine)
	outcome, err = restarted.Deliver(context.Background(), testRecord(validEnvelope))

	if err != nil || outcome != CommitOffset {
		t.Fatalf("redelivery after restart: outcome = %v, err = %v, want CommitOffset", outcome, err)
	}
	if effect.calls != 2 {
		t.Fatalf("effect consulted %d times, want 2 — the durable effect, not the consumer, decides idempotency", effect.calls)
	}
	if len(quarantine.isolated) != 0 {
		t.Fatalf("redelivery after restart was quarantined: %v", quarantine.isolated)
	}
}

func TestConsumerPropagatesTheEnvelopeTraceIDIntoTheEffectContext(t *testing.T) {
	effect := &traceRecordingEffect{}
	consumer := testConsumer(effect, &fakeQuarantine{})
	traced := strings.Replace(
		validEnvelope,
		`"orgId"`,
		`"traceparent":"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01","orgId"`,
		1,
	)

	if _, err := consumer.Deliver(context.Background(), testRecord(traced)); err != nil {
		t.Fatalf("Deliver returned error: %v", err)
	}

	if effect.traceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("effect saw trace ID %q, want the producing request's trace ID", effect.traceID)
	}
}

func TestConsumerLeavesTheEffectContextUncorrelatedWhenNoTraceparentIsPresent(t *testing.T) {
	effect := &traceRecordingEffect{}
	consumer := testConsumer(effect, &fakeQuarantine{})

	if _, err := consumer.Deliver(context.Background(), testRecord(validEnvelope)); err != nil {
		t.Fatalf("Deliver returned error: %v", err)
	}

	if effect.traceID != "" {
		t.Fatalf("effect saw invented trace ID %q, want none for an event with no originating request", effect.traceID)
	}
}

type traceRecordingEffect struct {
	traceID string
}

func (fake *traceRecordingEffect) Apply(ctx context.Context, _ event.Envelope) error {
	if correlation := requestcontext.GetCorrelation(ctx); correlation != nil {
		fake.traceID = correlation.TraceID
	}
	return nil
}
