package repository_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"seta-im-intern/go-asset-core/internal/eventing/event"
	"seta-im-intern/go-asset-core/internal/eventing/media"
	"seta-im-intern/go-asset-core/internal/eventing/outbox"
	"seta-im-intern/go-asset-core/internal/repository"
)

const testOutboxLeaseTTL = 30 * time.Second

func mustParseEnvelope(t *testing.T, value []byte) event.Envelope {
	t.Helper()
	envelope, err := event.Parse(value, []int{media.SchemaVersion})
	if err != nil {
		t.Fatalf("the claimed payload is not a valid envelope: %v", err)
	}
	return envelope
}

func (fixture *mediaJobFixture) outboxStore(t *testing.T) outbox.Store {
	t.Helper()
	store, err := repository.NewMediaOutboxStore(fixture.db, repository.MediaOutboxStoreOptions{
		Topic:    "media-processing.v1",
		LeaseTTL: testOutboxLeaseTTL,
	})
	if err != nil {
		t.Fatalf("NewMediaOutboxStore: %v", err)
	}
	return store
}

func (fixture *mediaJobFixture) outboxRow(t *testing.T) struct {
	Status         string
	AttemptCount   int
	LastErrorCode  *string
	PublishedAt    *time.Time
	LeaseOwner     *string
	LeaseExpiresAt *time.Time
	NextAttemptAt  time.Time
} {
	t.Helper()
	var row struct {
		Status         string
		AttemptCount   int
		LastErrorCode  *string
		PublishedAt    *time.Time
		LeaseOwner     *string
		LeaseExpiresAt *time.Time
		NextAttemptAt  time.Time
	}
	if err := fixture.db.Raw(
		"SELECT status, attempt_count, last_error_code, published_at, lease_owner, lease_expires_at, next_attempt_at FROM media_job_outbox WHERE id = ?",
		fixture.outboxID,
	).Scan(&row).Error; err != nil {
		t.Fatalf("read outbox row: %v", err)
	}
	return row
}

func TestOutboxClaimLeasesDueRowsForTheRelay(t *testing.T) {
	fixture := newMediaJobFixture(t)
	store := fixture.outboxStore(t)

	claimed, err := store.Claim(fixture.ctx, "relay-a", 10)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}

	if len(claimed.Records) != 1 {
		t.Fatalf("records = %d, want 1", len(claimed.Records))
	}
	record := claimed.Records[0]
	if record.EventID.String() != fixture.outboxID {
		t.Errorf("eventId = %s, want %s", record.EventID, fixture.outboxID)
	}
	if record.Key != fixture.jobID {
		t.Errorf("record key = %q, want the job ID %q", record.Key, fixture.jobID)
	}
	if record.Topic != "media-processing.v1" {
		t.Errorf("topic = %q, want media-processing.v1", record.Topic)
	}
	if claimed.Lease.Owner != "relay-a" || claimed.Lease.ExpiresAt.IsZero() {
		t.Errorf("lease = %+v, want an owned lease with an expiry", claimed.Lease)
	}

	// The relay must be able to hand the payload straight to a publisher, so
	// what comes back has to be the record value as written.
	if _, err := media.Parse(mustParseEnvelope(t, record.Payload)); err != nil {
		t.Errorf("claimed payload is not a usable media record: %v", err)
	}

	if row := fixture.outboxRow(t); row.Status != "publishing" {
		t.Errorf("status = %q, want publishing", row.Status)
	}
}

// The relay settles a whole batch against the one Lease that Claim returned, so
// every row in that batch must carry exactly that expiry. A per-row clock would
// leave rows the relay could publish but never mark published.
func TestOutboxClaimGivesEveryRowInABatchTheSameLease(t *testing.T) {
	fixture := newMediaJobFixture(t)
	fixture.seedAdditionalJobsWithOutboxRows(t, 4)
	store := fixture.outboxStore(t)

	claimed, err := store.Claim(fixture.ctx, "relay-a", 10)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if len(claimed.Records) != 5 {
		t.Fatalf("records = %d, want 5", len(claimed.Records))
	}

	for _, record := range claimed.Records {
		settled, err := store.MarkPublished(fixture.ctx, record.EventID, claimed.Lease, time.Now().UTC())
		if err != nil {
			t.Fatalf("MarkPublished %s: %v", record.EventID, err)
		}
		if !settled {
			t.Errorf("event %s could not be settled under the batch lease it was claimed with", record.EventID)
		}
	}
}

func TestOutboxClaimSkipsRowsThatAreNotDue(t *testing.T) {
	fixture := newMediaJobFixture(t)
	if err := fixture.db.Exec(
		"UPDATE media_job_outbox SET next_attempt_at = now() + interval '1 hour' WHERE id = ?", fixture.outboxID,
	).Error; err != nil {
		t.Fatalf("delay the row: %v", err)
	}

	claimed, err := fixture.outboxStore(t).Claim(fixture.ctx, "relay-a", 10)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}

	if len(claimed.Records) != 0 {
		t.Errorf("records = %d, want a row that is not yet due to stay unclaimed", len(claimed.Records))
	}
}

func TestOutboxClaimDoesNotStealALiveLease(t *testing.T) {
	fixture := newMediaJobFixture(t)
	store := fixture.outboxStore(t)

	if _, err := store.Claim(fixture.ctx, "relay-a", 10); err != nil {
		t.Fatalf("first claim: %v", err)
	}

	claimed, err := store.Claim(fixture.ctx, "relay-b", 10)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if len(claimed.Records) != 0 {
		t.Errorf("records = %d, want a live lease to block a second relay", len(claimed.Records))
	}
}

// A relay that died mid-publication leaves a 'publishing' row behind. Once its
// lease expires the row must become claimable again, or delivery stops forever.
func TestOutboxClaimReclaimsAnExpiredPublishingLease(t *testing.T) {
	fixture := newMediaJobFixture(t)
	store := fixture.outboxStore(t)

	if _, err := store.Claim(fixture.ctx, "relay-a", 10); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if err := fixture.db.Exec(
		"UPDATE media_job_outbox SET lease_expires_at = now() - interval '1 second' WHERE id = ?", fixture.outboxID,
	).Error; err != nil {
		t.Fatalf("expire the lease: %v", err)
	}

	claimed, err := store.Claim(fixture.ctx, "relay-b", 10)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}

	if len(claimed.Records) != 1 {
		t.Fatalf("records = %d, want the abandoned row to be reclaimable", len(claimed.Records))
	}
	if claimed.Lease.Owner != "relay-b" {
		t.Errorf("lease owner = %q, want relay-b", claimed.Lease.Owner)
	}
}

func TestOutboxMarkPublishedSettlesTheClaimedRow(t *testing.T) {
	fixture := newMediaJobFixture(t)
	store := fixture.outboxStore(t)

	claimed, err := store.Claim(fixture.ctx, "relay-a", 10)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}

	publishedAt := time.Now().UTC()
	settled, err := store.MarkPublished(fixture.ctx, claimed.Records[0].EventID, claimed.Lease, publishedAt)
	if err != nil {
		t.Fatalf("MarkPublished: %v", err)
	}
	if !settled {
		t.Fatal("marking a held row published must apply")
	}

	row := fixture.outboxRow(t)
	if row.Status != "published" {
		t.Errorf("status = %q, want published", row.Status)
	}
	if row.PublishedAt == nil {
		t.Error("publishedAt must be stamped")
	}
	if row.LeaseOwner != nil || row.LeaseExpiresAt != nil {
		t.Error("a settled row must not keep its lease")
	}
}

// The relay treats false as a lost race, not a fault, so the store must report
// it rather than erroring.
func TestOutboxMarkPublishedReportsFalseForAStaleLease(t *testing.T) {
	fixture := newMediaJobFixture(t)
	store := fixture.outboxStore(t)

	claimed, err := store.Claim(fixture.ctx, "relay-a", 10)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	stale := outbox.Lease{Owner: "relay-a", ExpiresAt: claimed.Lease.ExpiresAt.Add(-time.Minute)}

	settled, err := store.MarkPublished(fixture.ctx, claimed.Records[0].EventID, stale, time.Now().UTC())
	if err != nil {
		t.Fatalf("MarkPublished: %v", err)
	}
	if settled {
		t.Error("a stale lease must not settle the row")
	}
	if status := fixture.outboxRow(t).Status; status != "publishing" {
		t.Errorf("status = %q, want the row to stay claimed by its real owner", status)
	}
}

func TestOutboxMarkPublishedReportsFalseForAnotherOwner(t *testing.T) {
	fixture := newMediaJobFixture(t)
	store := fixture.outboxStore(t)

	claimed, err := store.Claim(fixture.ctx, "relay-a", 10)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	impersonated := outbox.Lease{Owner: "relay-b", ExpiresAt: claimed.Lease.ExpiresAt}

	settled, err := store.MarkPublished(fixture.ctx, claimed.Records[0].EventID, impersonated, time.Now().UTC())
	if err != nil {
		t.Fatalf("MarkPublished: %v", err)
	}
	if settled {
		t.Error("another owner must not be able to settle the row")
	}
}

func TestOutboxRescheduleReturnsTheRowToThePendingPool(t *testing.T) {
	fixture := newMediaJobFixture(t)
	store := fixture.outboxStore(t)

	claimed, err := store.Claim(fixture.ctx, "relay-a", 10)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}

	nextAttemptAt := time.Now().UTC().Add(2 * time.Second)
	applied, err := store.Reschedule(fixture.ctx, claimed.Records[0].EventID, claimed.Lease, nextAttemptAt, 1, "PUBLISH_TIMEOUT")
	if err != nil {
		t.Fatalf("Reschedule: %v", err)
	}
	if !applied {
		t.Fatal("rescheduling a held row must apply")
	}

	row := fixture.outboxRow(t)
	if row.Status != "pending" {
		t.Errorf("status = %q, want pending", row.Status)
	}
	if row.AttemptCount != 1 {
		t.Errorf("attemptCount = %d, want 1", row.AttemptCount)
	}
	if row.LastErrorCode == nil || *row.LastErrorCode != "PUBLISH_TIMEOUT" {
		t.Errorf("lastErrorCode = %v, want PUBLISH_TIMEOUT", row.LastErrorCode)
	}
	if row.LeaseOwner != nil {
		t.Error("a rescheduled row must release its lease so any relay can retry it")
	}
	if row.PublishedAt != nil {
		t.Error("rescheduling must never mark a row published")
	}
}

func TestOutboxRescheduleReportsFalseForAStaleLease(t *testing.T) {
	fixture := newMediaJobFixture(t)
	store := fixture.outboxStore(t)

	claimed, err := store.Claim(fixture.ctx, "relay-a", 10)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	stale := outbox.Lease{Owner: "relay-a", ExpiresAt: claimed.Lease.ExpiresAt.Add(-time.Minute)}

	applied, err := store.Reschedule(fixture.ctx, claimed.Records[0].EventID, stale, time.Now().UTC(), 1, "PUBLISH_FAILED")
	if err != nil {
		t.Fatalf("Reschedule: %v", err)
	}
	if applied {
		t.Error("a stale lease must not reschedule the row")
	}
}

// The partial unique index is what lets the relay's per-key claim rule hold, so
// it is worth asserting directly rather than assuming.
func TestOutboxRejectsASecondUnpublishedRowForTheSameJob(t *testing.T) {
	fixture := newMediaJobFixture(t)

	err := fixture.db.Exec(
		`INSERT INTO media_job_outbox (id, job_id, event_type, schema_version, payload, status, next_attempt_at)
		 VALUES (?, ?, ?, 1, '{}'::jsonb, 'pending', now())`,
		uuid.NewString(), fixture.jobID, media.EventType,
	).Error

	if err == nil {
		t.Fatal("a second unpublished row for one job must be rejected")
	}
}
