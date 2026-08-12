package outbox

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeStore struct {
	uncommitted      []Record
	claimable        []Record
	claimedBy        []string
	claimLimits      []int
	published        []uuid.UUID
	rescheduled      []uuid.UUID
	claimErr         error
	markErr          error
	loseMark         bool
	loseReschedule   bool
	calls            *[]string
	leaseExpiresAt   time.Time
	settlementLeases []Lease
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

func (fake *fakeStore) Claim(_ context.Context, owner string, limit int) (Claimed, error) {
	fake.claimedBy = append(fake.claimedBy, owner)
	fake.claimLimits = append(fake.claimLimits, limit)
	if fake.claimErr != nil {
		return Claimed{}, fake.claimErr
	}
	claimed := fake.claimable
	if len(claimed) > limit {
		claimed = claimed[:limit]
	}
	fake.claimable = fake.claimable[len(claimed):]
	leaseExpiresAt := fake.leaseExpiresAt
	if leaseExpiresAt.IsZero() {
		leaseExpiresAt = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return Claimed{Records: claimed, Lease: Lease{Owner: owner, ExpiresAt: leaseExpiresAt}}, nil
}

func (fake *fakeStore) MarkPublished(_ context.Context, eventID uuid.UUID, lease Lease, _ time.Time) (bool, error) {
	record(fake.calls, "mark:"+eventID.String())
	fake.settlementLeases = append(fake.settlementLeases, lease)
	if fake.markErr != nil {
		return false, fake.markErr
	}
	if fake.loseMark {
		return false, nil
	}
	fake.published = append(fake.published, eventID)
	return true, nil
}

func (fake *fakeStore) Reschedule(_ context.Context, eventID uuid.UUID, lease Lease, _ time.Time, _ int, _ string) (bool, error) {
	record(fake.calls, "reschedule:"+eventID.String())
	fake.settlementLeases = append(fake.settlementLeases, lease)
	if fake.loseReschedule {
		return false, nil
	}
	fake.rescheduled = append(fake.rescheduled, eventID)
	return true, nil
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

func (fake *leasedStore) Claim(_ context.Context, owner string, _ int) (Claimed, error) {
	fake.claimsBy = append(fake.claimsBy, owner)
	if fake.marked {
		return Claimed{}, nil
	}
	return Claimed{
		Records: []Record{fake.record},
		Lease: Lease{
			Owner:     owner,
			ExpiresAt: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC),
		},
	}, nil
}

func (fake *leasedStore) MarkPublished(_ context.Context, _ uuid.UUID, _ Lease, _ time.Time) (bool, error) {
	if fake.markErr != nil {
		return false, fake.markErr
	}
	fake.marked = true
	return true, nil
}

func (fake *leasedStore) Reschedule(_ context.Context, _ uuid.UUID, _ Lease, _ time.Time, _ int, _ string) (bool, error) {
	return true, nil
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

func TestRelayBacksOffExponentiallyUsingTheTransportAttemptItPasses(t *testing.T) {
	claimedAt := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	retried := testRecord("job-1")
	retried.AttemptCount = 3
	store := &attemptRecordingStore{claimable: []Record{retried}}

	relay := NewRelay(store, &fakePublisher{err: errPublishFailed}, RelayOptions{
		Owner:       "relay-1",
		BatchSize:   10,
		BaseBackoff: time.Second,
		MaxBackoff:  time.Minute,
		Now:         fixedClock(claimedAt),
		Jitter:      func(backoff time.Duration) time.Duration { return backoff },
	})
	if _, err := relay.DrainOnce(context.Background()); err != nil {
		t.Fatalf("DrainOnce returned error: %v", err)
	}

	if store.attemptCounts[0] != 4 {
		t.Fatalf("rescheduled attempt count = %d, want 4 so the adapter persists the transport attempt the relay computed", store.attemptCounts[0])
	}
	if delay := store.nextAttempts[0].Sub(claimedAt); delay != 8*time.Second {
		t.Fatalf("backoff = %v, want 8s (1s doubled three times) rather than a flat base delay", delay)
	}
}

func TestRelayBoundsItsDrainByTheLeaseTheDatabaseStamped(t *testing.T) {
	ResetMetricsForTests()
	t.Cleanup(ResetMetricsForTests)

	claimedAt := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	clock := claimedAt
	store := &attemptRecordingStore{
		claimable:      []Record{testRecord("job-1"), testRecord("job-2"), testRecord("job-3")},
		leaseExpiresAt: claimedAt.Add(6 * time.Second),
	}
	publisher := &clockAdvancingPublisher{clock: &clock, step: 4 * time.Second}

	relay := NewRelay(store, publisher, RelayOptions{
		Owner:       "relay-1",
		BatchSize:   10,
		LeaseMargin: 3 * time.Second,
		Now:         func() time.Time { return clock },
	})

	settled, err := relay.DrainOnce(context.Background())
	if err != nil {
		t.Fatalf("DrainOnce returned error: %v", err)
	}

	if settled != 1 {
		t.Fatalf("settled = %d, want 1 — the relay must use the database-stamped 6s lease", settled)
	}
	if Metrics().LeaseExhaustedTotal != 1 {
		t.Fatalf("LeaseExhaustedTotal = %d, want 1", Metrics().LeaseExhaustedTotal)
	}
}

type attemptRecordingStore struct {
	claimable      []Record
	leaseExpiresAt time.Time
	omitLease      bool
	attemptCounts  []int
	nextAttempts   []time.Time
}

func (fake *attemptRecordingStore) Claim(_ context.Context, owner string, limit int) (Claimed, error) {
	claimed := fake.claimable
	if len(claimed) > limit {
		claimed = claimed[:limit]
	}
	fake.claimable = fake.claimable[len(claimed):]
	leaseExpiresAt := fake.leaseExpiresAt
	if leaseExpiresAt.IsZero() && !fake.omitLease {
		leaseExpiresAt = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return Claimed{Records: claimed, Lease: Lease{Owner: owner, ExpiresAt: leaseExpiresAt}}, nil
}

func (fake *attemptRecordingStore) MarkPublished(_ context.Context, _ uuid.UUID, _ Lease, _ time.Time) (bool, error) {
	return true, nil
}

func (fake *attemptRecordingStore) Reschedule(_ context.Context, _ uuid.UUID, _ Lease, nextAttemptAt time.Time, attemptCount int, _ string) (bool, error) {
	fake.attemptCounts = append(fake.attemptCounts, attemptCount)
	fake.nextAttempts = append(fake.nextAttempts, nextAttemptAt)
	return true, nil
}

func TestRelayFailsClosedWhenAClaimHasNoLeaseExpiry(t *testing.T) {
	store := &attemptRecordingStore{
		claimable: []Record{testRecord("job-1")},
		omitLease: true,
	}
	publisher := &fakePublisher{}

	settled, err := NewRelay(store, publisher, RelayOptions{
		Owner:     "relay-1",
		BatchSize: 10,
	}).DrainOnce(context.Background())

	if !errors.Is(err, ErrMissingLeaseExpiry) {
		t.Fatalf("err = %v, want ErrMissingLeaseExpiry", err)
	}
	if settled != 0 || len(publisher.publishedKeys) != 0 {
		t.Fatalf("settled = %d, published = %v; a lease-less claim must never be published", settled, publisher.publishedKeys)
	}
}

func TestRelayBindsSuccessfulSettlementToTheExactClaimLease(t *testing.T) {
	expiresAt := time.Date(2026, 8, 12, 10, 1, 0, 0, time.UTC)
	store := &fakeStore{
		claimable:      []Record{testRecord("job-1")},
		leaseExpiresAt: expiresAt,
	}

	_, err := NewRelay(store, &fakePublisher{}, RelayOptions{
		Owner:     "relay-1",
		BatchSize: 10,
		Now:       fixedClock(expiresAt.Add(-time.Minute)),
	}).DrainOnce(context.Background())
	if err != nil {
		t.Fatalf("DrainOnce returned error: %v", err)
	}

	want := Lease{Owner: "relay-1", ExpiresAt: expiresAt}
	if len(store.settlementLeases) != 1 || store.settlementLeases[0] != want {
		t.Fatalf("settlement leases = %+v, want exact claim lease %+v", store.settlementLeases, want)
	}
}

func TestRelayRejectsAStaleSettlementThatUpdatesNoRow(t *testing.T) {
	for name, testCase := range map[string]struct {
		publisher Publisher
		store     *fakeStore
	}{
		"mark published": {
			publisher: &fakePublisher{},
			store:     &fakeStore{claimable: []Record{testRecord("job-1"), testRecord("job-2")}, loseMark: true},
		},
		"reschedule": {
			publisher: &fakePublisher{err: errPublishFailed},
			store:     &fakeStore{claimable: []Record{testRecord("job-1"), testRecord("job-2")}, loseReschedule: true},
		},
	} {
		t.Run(name, func(t *testing.T) {
			settled, err := testRelay(testCase.store, testCase.publisher).DrainOnce(context.Background())
			if !errors.Is(err, ErrLeaseLost) {
				t.Fatalf("err = %v, want ErrLeaseLost when the conditional settlement matched no row", err)
			}
			if settled != 0 {
				t.Fatalf("settled = %d, want 0 after losing the lease", settled)
			}
			if len(testCase.store.settlementLeases) != 1 {
				t.Fatalf("settlement attempts = %d, want 1; losing a batch lease must stop before the next row", len(testCase.store.settlementLeases))
			}
		})
	}
}

func TestRelayRejectsAClaimOwnedByAnotherRelay(t *testing.T) {
	record := testRecord("job-1")
	store := &wrongOwnerStore{record: record}
	publisher := &fakePublisher{}

	settled, err := NewRelay(store, publisher, RelayOptions{
		Owner:     "relay-1",
		BatchSize: 10,
	}).DrainOnce(context.Background())

	if !errors.Is(err, ErrLeaseOwnerMismatch) {
		t.Fatalf("err = %v, want ErrLeaseOwnerMismatch", err)
	}
	if settled != 0 || len(publisher.publishedKeys) != 0 {
		t.Fatalf("settled = %d, published = %v; a foreign lease must never authorize publication", settled, publisher.publishedKeys)
	}
}

type wrongOwnerStore struct {
	record Record
}

func (store *wrongOwnerStore) Claim(_ context.Context, _ string, _ int) (Claimed, error) {
	return Claimed{
		Records: []Record{store.record},
		Lease: Lease{
			Owner:     "relay-2",
			ExpiresAt: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC),
		},
	}, nil
}

func (*wrongOwnerStore) MarkPublished(context.Context, uuid.UUID, Lease, time.Time) (bool, error) {
	return true, nil
}

func (*wrongOwnerStore) Reschedule(context.Context, uuid.UUID, Lease, time.Time, int, string) (bool, error) {
	return true, nil
}

type keyedRow struct {
	record    Record
	published bool
	lease     Lease
}

// keySerialStore models the Store contract that SQL adapters must implement:
// an earlier unpublished row blocks every later row for the same topic/key,
// including while that earlier row is locked or leased by another relay.
type keySerialStore struct {
	mu   sync.Mutex
	rows []keyedRow
	now  time.Time
}

func (store *keySerialStore) Claim(_ context.Context, owner string, limit int) (Claimed, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	expiresAt := store.now.Add(time.Minute)
	claimed := Claimed{Lease: Lease{Owner: owner, ExpiresAt: expiresAt}}
	for index := range store.rows {
		row := &store.rows[index]
		if row.published || store.hasEarlierUnpublished(index) {
			continue
		}
		if !row.lease.ExpiresAt.IsZero() && store.now.Before(row.lease.ExpiresAt) {
			continue
		}
		row.lease = claimed.Lease
		claimed.Records = append(claimed.Records, row.record)
		if len(claimed.Records) == limit {
			break
		}
	}
	return claimed, nil
}

func (store *keySerialStore) hasEarlierUnpublished(index int) bool {
	row := store.rows[index]
	for earlier := 0; earlier < index; earlier++ {
		candidate := store.rows[earlier]
		if !candidate.published && candidate.record.Topic == row.record.Topic && candidate.record.Key == row.record.Key {
			return true
		}
	}
	return false
}

func (store *keySerialStore) MarkPublished(_ context.Context, eventID uuid.UUID, lease Lease, _ time.Time) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for index := range store.rows {
		row := &store.rows[index]
		if row.record.EventID == eventID && row.lease == lease && store.now.Before(row.lease.ExpiresAt) {
			row.published = true
			row.lease = Lease{}
			return true, nil
		}
	}
	return false, nil
}

func (store *keySerialStore) Reschedule(_ context.Context, eventID uuid.UUID, lease Lease, _ time.Time, _ int, _ string) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for index := range store.rows {
		row := &store.rows[index]
		if row.record.EventID == eventID && row.lease == lease && store.now.Before(row.lease.ExpiresAt) {
			row.lease = Lease{}
			return true, nil
		}
	}
	return false, nil
}

type gatePublisher struct {
	started chan string
	release chan struct{}
	keys    []string
}

func (publisher *gatePublisher) Publish(_ context.Context, _ string, key string, _ []byte) error {
	publisher.started <- key
	<-publisher.release
	publisher.keys = append(publisher.keys, key)
	return nil
}

func TestConcurrentRelaysCannotClaimANewerUnpublishedRowForTheSameKey(t *testing.T) {
	older := testRecord("job-1")
	older.EnqueuedAt = time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	newer := testRecord("job-1")
	newer.EnqueuedAt = older.EnqueuedAt.Add(time.Second)
	store := &keySerialStore{
		rows: []keyedRow{{record: older}, {record: newer}},
		now:  older.EnqueuedAt,
	}
	firstPublisher := &gatePublisher{started: make(chan string, 1), release: make(chan struct{})}
	firstDone := make(chan error, 1)
	go func() {
		_, err := NewRelay(store, firstPublisher, RelayOptions{Owner: "relay-1", BatchSize: 1, Now: fixedClock(store.now)}).DrainOnce(context.Background())
		firstDone <- err
	}()

	select {
	case <-firstPublisher.started:
	case <-time.After(time.Second):
		t.Fatal("first relay did not start publishing the head row")
	}

	secondPublisher := &fakePublisher{}
	settled, err := NewRelay(store, secondPublisher, RelayOptions{Owner: "relay-2", BatchSize: 1, Now: fixedClock(store.now)}).DrainOnce(context.Background())
	if err != nil {
		t.Fatalf("second relay returned error while the key head was leased: %v", err)
	}
	if settled != 0 || len(secondPublisher.publishedKeys) != 0 {
		t.Fatalf("second relay settled %d and published %v; the leased head must block the newer same-key row", settled, secondPublisher.publishedKeys)
	}

	close(firstPublisher.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first relay returned error: %v", err)
	}

	settled, err = NewRelay(store, secondPublisher, RelayOptions{Owner: "relay-2", BatchSize: 1, Now: fixedClock(store.now)}).DrainOnce(context.Background())
	if err != nil || settled != 1 {
		t.Fatalf("second relay after head settlement: settled = %d, err = %v; want the newer row", settled, err)
	}
	if len(secondPublisher.publishedKeys) != 1 || secondPublisher.publishedKeys[0] != "job-1" {
		t.Fatalf("published keys = %v, want the unblocked newer row", secondPublisher.publishedKeys)
	}
}
