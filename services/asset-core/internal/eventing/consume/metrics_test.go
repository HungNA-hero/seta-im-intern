package consume

import (
	"context"
	"errors"
	"testing"
)

func TestConsumeMetricsCountEachDeliveryOutcome(t *testing.T) {
	ResetMetricsForTests()
	t.Cleanup(ResetMetricsForTests)

	applied := &fakeEffect{}
	if _, err := testConsumer(applied, &fakeQuarantine{}).Deliver(context.Background(), testRecord(validEnvelope)); err != nil {
		t.Fatalf("applied delivery: %v", err)
	}

	duplicate := &fakeEffect{err: ErrAlreadyApplied}
	if _, err := testConsumer(duplicate, &fakeQuarantine{}).Deliver(context.Background(), testRecord(validEnvelope)); err != nil {
		t.Fatalf("duplicate delivery: %v", err)
	}

	transient := &fakeEffect{err: errors.New("database unavailable")}
	if _, err := testConsumer(transient, &fakeQuarantine{}).Deliver(context.Background(), testRecord(validEnvelope)); err == nil {
		t.Fatal("expected a transient failure")
	}

	if _, err := testConsumer(&fakeEffect{}, &fakeQuarantine{}).Deliver(context.Background(), testRecord(`bad`)); err != nil {
		t.Fatalf("quarantined delivery: %v", err)
	}

	metrics := Metrics()
	if metrics.AppliedTotal != 1 || metrics.DuplicateTotal != 1 || metrics.TransientFailureTotal != 1 || metrics.QuarantinedTotal != 1 {
		t.Fatalf("metrics = %+v, want one of each outcome", metrics)
	}
}
