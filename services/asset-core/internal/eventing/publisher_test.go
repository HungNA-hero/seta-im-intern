package eventing

import (
	"context"
	"errors"
	"testing"

	"github.com/redis/go-redis/v9"
)

type fakeXAdder struct {
	streamID string
	err      error
	calls    int
	args     *redis.XAddArgs
}

func (fake *fakeXAdder) XAdd(_ context.Context, args *redis.XAddArgs) *redis.StringCmd {
	fake.calls++
	fake.args = args
	return redis.NewStringResult(fake.streamID, fake.err)
}

func testEnvelope() Envelope {
	return Envelope{
		EventID:       "event-1",
		EventType:     "folder.moved",
		SchemaVersion: 1,
		Source:        "asset-core",
		AggregateType: "folder",
		AggregateID:   "folder-1",
		OrgID:         "org-1",
		Data:          []byte(`{"folderId":"folder-1"}`),
	}
}

func TestPublishWithAccountsForSuccessfulXAdd(t *testing.T) {
	ResetMetricsForTests()
	t.Cleanup(ResetMetricsForTests)
	fake := &fakeXAdder{streamID: "1-0"}

	publishWith(testEnvelope(), fake)

	if fake.calls != 1 || fake.args == nil || fake.args.Stream != assetEventsStream {
		t.Fatalf("unexpected XADD call: calls=%d args=%#v", fake.calls, fake.args)
	}
	metrics := Metrics()
	if metrics.PublishSuccessTotal != 1 || metrics.PublishFailureTotal != 0 {
		t.Fatalf("unexpected metrics after success: %#v", metrics)
	}
}

func TestPublishWithAccountsForFailedXAddWithoutPropagating(t *testing.T) {
	ResetMetricsForTests()
	t.Cleanup(ResetMetricsForTests)
	fake := &fakeXAdder{err: errors.New("redis unavailable")}

	// Publishing is deliberately best-effort. A failed XADD is accounted for
	// and returns normally to the already-committed Asset operation.
	publishWith(testEnvelope(), fake)

	metrics := Metrics()
	if fake.calls != 1 {
		t.Fatalf("failed publication attempted XADD %d times, want exactly once", fake.calls)
	}
	if metrics.PublishSuccessTotal != 0 || metrics.PublishFailureTotal != 1 {
		t.Fatalf("unexpected metrics after failure: %#v", metrics)
	}
}
