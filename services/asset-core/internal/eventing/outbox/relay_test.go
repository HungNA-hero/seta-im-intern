package outbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeStore struct {
	uncommitted []Record
	claimable   []Record
	claimedBy   []string
	claimLimits []int
	published   []uuid.UUID
	rescheduled []uuid.UUID
	claimErr    error
	markErr     error
	calls       *[]string
}

func record(calls *[]string, call string) {
	if calls != nil {
		*calls = append(*calls, call)
	}
}

func (fake *fakeStore) commitTransaction() {
	fake.claimable = append(fake.claimable, fake.uncommitted...)
	fake.uncommitted = nil
}

func (fake *fakeStore) Claim(_ context.Context, owner string, limit int) ([]Record, error) {
	fake.claimedBy = append(fake.claimedBy, owner)
	fake.claimLimits = append(fake.claimLimits, limit)
	if fake.claimErr != nil {
		return nil, fake.claimErr
	}
	claimed := fake.claimable
	if len(claimed) > limit {
		claimed = claimed[:limit]
	}
	fake.claimable = fake.claimable[len(claimed):]
	return claimed, nil
}

func (fake *fakeStore) MarkPublished(_ context.Context, eventID uuid.UUID, _ time.Time) error {
	record(fake.calls, "mark:"+eventID.String())
	if fake.markErr != nil {
		return fake.markErr
	}
	fake.published = append(fake.published, eventID)
	return nil
}

func (fake *fakeStore) Reschedule(_ context.Context, eventID uuid.UUID, _ time.Time, _ string) error {
	record(fake.calls, "reschedule:"+eventID.String())
	fake.rescheduled = append(fake.rescheduled, eventID)
	return nil
}

type fakePublisher struct {
	publishedKeys []string
	err           error
	calls         *[]string
}

func (fake *fakePublisher) Publish(_ context.Context, _ string, key string, payload []byte) error {
	record(fake.calls, "publish:"+key)
	if fake.err != nil {
		return fake.err
	}
	fake.publishedKeys = append(fake.publishedKeys, key)
	_ = payload
	return nil
}

func testRecord(key string) Record {
	return Record{
		EventID: uuid.New(),
		Topic:   "media-processing.v1",
		Key:     key,
		Payload: []byte(`{"eventId":"x"}`),
	}
}

func testRelay(store Store, publisher Publisher) *Relay {
	return NewRelay(store, publisher, RelayOptions{Owner: "relay-1", BatchSize: 10})
}

func TestRelayMarksPublishedOnlyAfterTheBrokerAcknowledges(t *testing.T) {
	calls := []string{}
	outboxRecord := testRecord("job-1")
	store := &fakeStore{claimable: []Record{outboxRecord}, calls: &calls}
	publisher := &fakePublisher{calls: &calls}
	relay := testRelay(store, publisher)

	if _, err := relay.DrainOnce(context.Background()); err != nil {
		t.Fatalf("DrainOnce returned error: %v", err)
	}

	want := []string{"publish:job-1", "mark:" + outboxRecord.EventID.String()}
	if len(calls) != len(want) || calls[0] != want[0] || calls[1] != want[1] {
		t.Fatalf("call order = %v, want %v — the outbox row must never be marked before the broker acknowledges", calls, want)
	}
}

func TestRelayReschedulesAndDoesNotMarkWhenPublicationFails(t *testing.T) {
	outboxRecord := testRecord("job-1")
	store := &fakeStore{claimable: []Record{outboxRecord}}
	publisher := &fakePublisher{err: errPublishFailed}
	relay := testRelay(store, publisher)

	settled, err := relay.DrainOnce(context.Background())
	if err != nil {
		t.Fatalf("a failed publication is a normal retry, not a drain error: %v", err)
	}
	if settled != 0 {
		t.Fatalf("settled = %d, want 0 when the broker never acknowledged", settled)
	}
	if len(store.published) != 0 {
		t.Fatalf("relay marked %v published without a broker acknowledgement", store.published)
	}
	if len(store.rescheduled) != 1 || store.rescheduled[0] != outboxRecord.EventID {
		t.Fatalf("rescheduled = %v, want the unpublished event to stay due", store.rescheduled)
	}
}

func TestRelayReportsACrashBetweenAcknowledgementAndOutboxUpdateWithoutRescheduling(t *testing.T) {
	outboxRecord := testRecord("job-1")
	store := &fakeStore{claimable: []Record{outboxRecord}, markErr: errMarkFailed}
	publisher := &fakePublisher{}
	relay := testRelay(store, publisher)

	settled, err := relay.DrainOnce(context.Background())
	if err == nil {
		t.Fatal("DrainOnce returned no error when the outbox update failed after a broker acknowledgement")
	}
	if settled != 0 {
		t.Fatalf("settled = %d, want 0 when the outbox row was never updated", settled)
	}
	if len(store.rescheduled) != 0 {
		t.Fatalf("rescheduled = %v, want none — the lease expires and another relay reclaims the row", store.rescheduled)
	}
	if len(publisher.publishedKeys) != 1 {
		t.Fatalf("published keys = %v, want the record to have reached the broker exactly once", publisher.publishedKeys)
	}
}

func TestRelayClaimsUnderItsOwnLeaseOwnerInBoundedBatches(t *testing.T) {
	store := &fakeStore{claimable: []Record{testRecord("job-1"), testRecord("job-2"), testRecord("job-3")}}
	publisher := &fakePublisher{}
	relay := NewRelay(store, publisher, RelayOptions{Owner: "relay-1", BatchSize: 2})

	published, err := relay.DrainOnce(context.Background())
	if err != nil {
		t.Fatalf("DrainOnce returned error: %v", err)
	}

	if published != 2 {
		t.Fatalf("relay published %d records in one batch, want the configured batch size 2", published)
	}
	if len(store.claimLimits) != 1 || store.claimLimits[0] != 2 {
		t.Fatalf("claim limits = %v, want a single bounded claim of [2]", store.claimLimits)
	}
	if len(store.claimedBy) != 1 || store.claimedBy[0] != "relay-1" {
		t.Fatalf("claimed by %v, want the relay's own lease owner [relay-1]", store.claimedBy)
	}

	if _, err = relay.DrainOnce(context.Background()); err != nil {
		t.Fatalf("second DrainOnce returned error: %v", err)
	}
	if len(publisher.publishedKeys) != 3 {
		t.Fatalf("published %v across two drains, want all three records", publisher.publishedKeys)
	}
}

func TestRelayDoesNotPublishBeforeTheBusinessTransactionCommits(t *testing.T) {
	store := &fakeStore{uncommitted: []Record{testRecord("job-1")}}
	publisher := &fakePublisher{}
	relay := testRelay(store, publisher)

	published, err := relay.DrainOnce(context.Background())
	if err != nil {
		t.Fatalf("DrainOnce before commit returned error: %v", err)
	}
	if published != 0 || len(publisher.publishedKeys) != 0 {
		t.Fatalf("relay published %d uncommitted records, want 0", len(publisher.publishedKeys))
	}

	store.commitTransaction()

	published, err = relay.DrainOnce(context.Background())
	if err != nil {
		t.Fatalf("DrainOnce after commit returned error: %v", err)
	}
	if published != 1 {
		t.Fatalf("relay published %d records after commit, want 1", published)
	}
	if len(publisher.publishedKeys) != 1 || publisher.publishedKeys[0] != "job-1" {
		t.Fatalf("published keys = %v, want [job-1]", publisher.publishedKeys)
	}
}

var (
	errPublishFailed = errors.New("broker unavailable")
	errMarkFailed    = errors.New("outbox update failed")
)

type leasedStore struct {
	record   Record
	marked   bool
	markErr  error
	claimsBy []string
}

func (fake *leasedStore) Claim(_ context.Context, owner string, _ int) ([]Record, error) {
	fake.claimsBy = append(fake.claimsBy, owner)
	if fake.marked {
		return nil, nil
	}
	return []Record{fake.record}, nil
}

func (fake *leasedStore) MarkPublished(_ context.Context, _ uuid.UUID, _ time.Time) error {
	if fake.markErr != nil {
		return fake.markErr
	}
	fake.marked = true
	return nil
}

func (fake *leasedStore) Reschedule(_ context.Context, _ uuid.UUID, _ time.Time, _ string) error {
	return nil
}

type eventIDRecordingPublisher struct {
	seen []uuid.UUID
	next uuid.UUID
}

func (fake *eventIDRecordingPublisher) Publish(_ context.Context, _ string, _ string, _ []byte) error {
	fake.seen = append(fake.seen, fake.next)
	return nil
}

func TestARestartedRelayReclaimsTheLeaseAndRepublishesTheSameEventID(t *testing.T) {
	outboxRecord := testRecord("job-1")
	store := &leasedStore{record: outboxRecord, markErr: errMarkFailed}
	publisher := &eventIDRecordingPublisher{next: outboxRecord.EventID}

	crashed := NewRelay(store, publisher, RelayOptions{Owner: "relay-1", BatchSize: 10})
	if _, err := crashed.DrainOnce(context.Background()); err == nil {
		t.Fatal("expected the outbox update to fail after the broker acknowledged")
	}
	if store.marked {
		t.Fatal("outbox row was marked published despite the failed update")
	}

	store.markErr = nil
	restarted := NewRelay(store, publisher, RelayOptions{Owner: "relay-2", BatchSize: 10})
	settled, err := restarted.DrainOnce(context.Background())
	if err != nil {
		t.Fatalf("restarted relay returned error: %v", err)
	}

	if settled != 1 || !store.marked {
		t.Fatalf("settled = %d, marked = %v, want the restarted relay to settle the row", settled, store.marked)
	}
	if len(publisher.seen) != 2 || publisher.seen[0] != publisher.seen[1] {
		t.Fatalf("published event IDs = %v, want the same event ID twice so consumers can deduplicate", publisher.seen)
	}
	if len(store.claimsBy) != 2 || store.claimsBy[0] != "relay-1" || store.claimsBy[1] != "relay-2" {
		t.Fatalf("claims = %v, want the expired lease reclaimed by a different owner", store.claimsBy)
	}
}
