package outbox

import (
	"context"
	"testing"
	"time"
)

func fixedClock(at time.Time) func() time.Time {
	return func() time.Time { return at }
}

func TestRelayMetricsCountPublicationOutcomes(t *testing.T) {
	ResetMetricsForTests()
	t.Cleanup(ResetMetricsForTests)

	store := &fakeStore{claimable: []Record{testRecord("job-1"), testRecord("job-2")}}
	relay := testRelay(store, &fakePublisher{})
	if _, err := relay.DrainOnce(context.Background()); err != nil {
		t.Fatalf("DrainOnce returned error: %v", err)
	}

	failing := &fakeStore{claimable: []Record{testRecord("job-3")}}
	if _, err := testRelay(failing, &fakePublisher{err: errPublishFailed}).DrainOnce(context.Background()); err != nil {
		t.Fatalf("DrainOnce returned error: %v", err)
	}

	unmarked := &fakeStore{claimable: []Record{testRecord("job-4")}, markErr: errMarkFailed}
	if _, err := testRelay(unmarked, &fakePublisher{}).DrainOnce(context.Background()); err == nil {
		t.Fatal("expected an error when the outbox update failed")
	}

	metrics := Metrics()
	if metrics.PublishedTotal != 3 {
		t.Fatalf("PublishedTotal = %d, want 3 — it counts broker acknowledgements, including the record whose outbox update then failed", metrics.PublishedTotal)
	}
	if metrics.PublishFailureTotal != 1 {
		t.Fatalf("PublishFailureTotal = %d, want 1", metrics.PublishFailureTotal)
	}
	if metrics.OutboxUpdateFailureTotal != 1 {
		t.Fatalf("OutboxUpdateFailureTotal = %d, want 1", metrics.OutboxUpdateFailureTotal)
	}
}

func TestRelayMetricsReportDeliveryLagFromEnqueueToPublication(t *testing.T) {
	ResetMetricsForTests()
	t.Cleanup(ResetMetricsForTests)

	publishedAt := time.Date(2026, 8, 12, 10, 0, 2, 0, time.UTC)
	delayed := testRecord("job-1")
	delayed.EnqueuedAt = publishedAt.Add(-2 * time.Second)

	store := &fakeStore{claimable: []Record{delayed}}
	relay := NewRelay(store, &fakePublisher{}, RelayOptions{Owner: "relay-1", BatchSize: 10, Now: fixedClock(publishedAt)})
	if _, err := relay.DrainOnce(context.Background()); err != nil {
		t.Fatalf("DrainOnce returned error: %v", err)
	}

	if lag := Metrics().LastDeliveryLagMillis; lag != 2000 {
		t.Fatalf("LastDeliveryLagMillis = %d, want 2000", lag)
	}
}

func TestRelayStopsDrainingBeforeItsLeaseExpires(t *testing.T) {
	ResetMetricsForTests()
	t.Cleanup(ResetMetricsForTests)

	claimedAt := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	clock := claimedAt
	store := &fakeStore{
		claimable:      []Record{testRecord("job-1"), testRecord("job-2"), testRecord("job-3")},
		leaseExpiresAt: claimedAt.Add(10 * time.Second),
	}
	slowPublisher := &clockAdvancingPublisher{clock: &clock, step: 4 * time.Second}

	relay := NewRelay(store, slowPublisher, RelayOptions{
		Owner:       "relay-1",
		BatchSize:   10,
		LeaseMargin: 3 * time.Second,
		Now:         func() time.Time { return clock },
	})

	settled, err := relay.DrainOnce(context.Background())
	if err != nil {
		t.Fatalf("DrainOnce returned error: %v", err)
	}

	if settled != 2 {
		t.Fatalf("settled = %d, want 2 — the third record would publish under an expired lease", settled)
	}
	if len(slowPublisher.published) != 2 {
		t.Fatalf("published %v, want the relay to stop before its lease expired", slowPublisher.published)
	}
	if Metrics().LeaseExhaustedTotal != 1 {
		t.Fatalf("LeaseExhaustedTotal = %d, want 1", Metrics().LeaseExhaustedTotal)
	}
}

type clockAdvancingPublisher struct {
	clock     *time.Time
	step      time.Duration
	published []string
}

func (fake *clockAdvancingPublisher) Publish(_ context.Context, _ string, key string, _ []byte) error {
	*fake.clock = fake.clock.Add(fake.step)
	fake.published = append(fake.published, key)
	return nil
}

type deadlineRecordingPublisher struct {
	deadlines   []time.Time
	hadDeadline []bool
}

func (fake *deadlineRecordingPublisher) Publish(ctx context.Context, _ string, _ string, _ []byte) error {
	deadline, ok := ctx.Deadline()
	fake.deadlines = append(fake.deadlines, deadline)
	fake.hadDeadline = append(fake.hadDeadline, ok)
	return nil
}

func TestRelayGivesEachPublishADeadlineBeforeTheLeaseMargin(t *testing.T) {
	claimedAt := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	leaseExpiresAt := claimedAt.Add(6 * time.Second)
	store := &attemptRecordingStore{claimable: []Record{testRecord("job-1")}, leaseExpiresAt: leaseExpiresAt}
	publisher := &deadlineRecordingPublisher{}

	relay := NewRelay(store, publisher, RelayOptions{
		Owner:       "relay-1",
		BatchSize:   10,
		LeaseMargin: 2 * time.Second,
		Now:         fixedClock(claimedAt),
	})
	if _, err := relay.DrainOnce(context.Background()); err != nil {
		t.Fatalf("DrainOnce returned error: %v", err)
	}

	if !publisher.hadDeadline[0] {
		t.Fatal("publish ran with no deadline; a write outlasting the lease can land after another relay reclaims the row")
	}
	wantDeadline := leaseExpiresAt.Add(-2 * time.Second)
	if !publisher.deadlines[0].Equal(wantDeadline) {
		t.Fatalf("publish deadline = %v, want %v so acknowledgement marking retains the lease margin", publisher.deadlines[0], wantDeadline)
	}
}
