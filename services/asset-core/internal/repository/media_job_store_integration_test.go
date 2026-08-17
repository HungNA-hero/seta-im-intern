package repository_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/eventing/event"
	"seta-im-intern/go-asset-core/internal/eventing/media"
	"seta-im-intern/go-asset-core/internal/repository"
)

const (
	testMaxAttempts     = 3
	testJobLeaseExpiry  = 30 * time.Second
	testJobLeaseRenewal = 10 * time.Second
)

// mediaJobFixture seeds one asset with one pending version, one queued job, and
// one unpublished outbox row — the exact shape a committed upload leaves behind.
type mediaJobFixture struct {
	t         *testing.T
	db        *gorm.DB
	ctx       context.Context
	orgID     string
	userID    string
	assetID   string
	uploadID  string
	versionID string
	jobID     string
	outboxID  string
}

func newMediaJobFixture(t *testing.T) *mediaJobFixture {
	t.Helper()
	dsn := os.Getenv("ASSET_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ASSET_TEST_DATABASE_URL is not set")
	}
	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	tx := database.Begin()
	if tx.Error != nil {
		t.Fatalf("begin rollback-only transaction: %v", tx.Error)
	}
	t.Cleanup(func() {
		if err := tx.Rollback().Error; err != nil && !errors.Is(err, gorm.ErrInvalidTransaction) {
			t.Errorf("rollback integration transaction: %v", err)
		}
	})

	fixture := &mediaJobFixture{
		t:         t,
		db:        tx,
		ctx:       context.Background(),
		orgID:     uuid.NewString(),
		userID:    uuid.NewString(),
		assetID:   uuid.NewString(),
		uploadID:  uuid.NewString(),
		versionID: uuid.NewString(),
		jobID:     uuid.NewString(),
		outboxID:  uuid.NewString(),
	}
	fixture.seed()
	return fixture
}

func (fixture *mediaJobFixture) seed() {
	fixture.t.Helper()

	folderID := uuid.NewString()
	statements := []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO organization_ref (org_id) VALUES (?)", []any{fixture.orgID}},
		{"INSERT INTO user_ref (user_id) VALUES (?)", []any{fixture.userID}},
		{
			"INSERT INTO folders (id, org_id, path, name, created_by) VALUES (?, ?, ?::ltree, ?, ?)",
			[]any{folderID, fixture.orgID, strings.ReplaceAll(folderID, "-", ""), "Media Jobs", fixture.userID},
		},
		{
			"INSERT INTO metadata_items (id, folder_id, title, created_by) VALUES (?, ?, ?, ?)",
			[]any{fixture.assetID, folderID, "Media asset", fixture.userID},
		},
		{
			`INSERT INTO media_upload_sessions
			 (id, org_id, asset_id, requested_by, idempotency_key, request_fingerprint, state,
			  original_filename, declared_content_type, file_extension, expected_size_bytes,
			  declared_checksum_sha256, raw_object_key, credential_expires_at, session_expires_at, committed_at)
			 VALUES (?, ?, ?, ?, ?, decode(repeat('2a', 32), 'hex'), 'committed', 'photo.png', 'image/png', 'png', 1024,
			         decode(repeat('2a', 32), 'hex'), ?, now() + interval '1 hour', now() + interval '24 hours', now())`,
			[]any{fixture.uploadID, fixture.orgID, fixture.assetID, fixture.userID, uuid.NewString(),
				"raw/" + fixture.orgID + "/" + fixture.assetID + "/" + fixture.uploadID + "/original.png"},
		},
		{
			`INSERT INTO asset_media_versions
			 (id, org_id, asset_id, upload_id, requested_by, status, raw_object_key, declared_content_type, original_size_bytes)
			 VALUES (?, ?, ?, ?, ?, 'pending', ?, 'image/png', 1024)`,
			[]any{fixture.versionID, fixture.orgID, fixture.assetID, fixture.uploadID, fixture.userID,
				"raw/" + fixture.orgID + "/" + fixture.assetID + "/" + fixture.uploadID + "/original.png"},
		},
		{
			`INSERT INTO media_processing_jobs (id, org_id, asset_id, version_id, status, next_attempt_at)
			 VALUES (?, ?, ?, ?, 'queued', now())`,
			[]any{fixture.jobID, fixture.orgID, fixture.assetID, fixture.versionID},
		},
	}
	for _, statement := range statements {
		if err := fixture.db.Exec(statement.sql, statement.args...).Error; err != nil {
			fixture.t.Fatalf("seed: %v", err)
		}
	}
	fixture.seedOutboxRow()
}

func (fixture *mediaJobFixture) seedOutboxRow() {
	fixture.t.Helper()

	payload, err := media.Marshal(
		event.Envelope{EventID: fixture.outboxID, OrgID: fixture.orgID, OccurredAt: time.Now().UTC()},
		media.Payload{AssetID: fixture.assetID, UploadID: fixture.uploadID, VersionID: fixture.versionID, JobID: fixture.jobID},
	)
	if err != nil {
		fixture.t.Fatalf("marshal outbox payload: %v", err)
	}
	if err := fixture.db.Exec(
		`INSERT INTO media_job_outbox (id, job_id, event_type, schema_version, payload, status, next_attempt_at)
		 VALUES (?, ?, ?, 1, ?::jsonb, 'pending', now())`,
		fixture.outboxID, fixture.jobID, media.EventType, string(payload),
	).Error; err != nil {
		fixture.t.Fatalf("seed outbox row: %v", err)
	}
}

func (fixture *mediaJobFixture) store() interface {
	ClaimJob(context.Context, string, string) (domain.MediaProcessingJob, domain.JobLease, error)
	RenewLease(context.Context, string, domain.JobLease) (domain.JobLease, error)
	ReleaseLease(context.Context, string, domain.JobLease) (bool, error)
} {
	return repository.NewMediaJobStore(
		fixture.db,
		domain.MediaLeasePolicy{RenewalInterval: testJobLeaseRenewal, Expiry: testJobLeaseExpiry},
		testMaxAttempts,
	)
}

// seedAdditionalJobsWithOutboxRows adds more queued jobs on the same asset, each
// with its own unpublished outbox row, so a claim can return a real batch.
func (fixture *mediaJobFixture) seedAdditionalJobsWithOutboxRows(t *testing.T, count int) {
	t.Helper()

	for index := 0; index < count; index++ {
		versionID, jobID, outboxID := uuid.NewString(), uuid.NewString(), uuid.NewString()
		uploadID := uuid.NewString()

		if err := fixture.db.Exec(
			`INSERT INTO media_upload_sessions
			 (id, org_id, asset_id, requested_by, idempotency_key, request_fingerprint, state,
			  original_filename, declared_content_type, file_extension, expected_size_bytes,
			  declared_checksum_sha256, raw_object_key, credential_expires_at, session_expires_at, committed_at)
			 VALUES (?, ?, ?, ?, ?, decode(repeat('2a', 32), 'hex'), 'committed', 'photo.png', 'image/png', 'png', 1024,
			         decode(repeat('2a', 32), 'hex'), ?, now() + interval '1 hour', now() + interval '24 hours', now())`,
			uploadID, fixture.orgID, fixture.assetID, fixture.userID, uuid.NewString(),
			"raw/"+fixture.orgID+"/"+fixture.assetID+"/"+uploadID+"/original.png",
		).Error; err != nil {
			t.Fatalf("seed extra session: %v", err)
		}
		if err := fixture.db.Exec(
			`INSERT INTO asset_media_versions
			 (id, org_id, asset_id, upload_id, requested_by, status, raw_object_key, declared_content_type, original_size_bytes)
			 VALUES (?, ?, ?, ?, ?, 'completed', ?, 'image/png', 1024)`,
			versionID, fixture.orgID, fixture.assetID, uploadID, fixture.userID,
			"raw/"+fixture.orgID+"/"+fixture.assetID+"/"+uploadID+"/original.png",
		).Error; err != nil {
			t.Fatalf("seed extra version: %v", err)
		}
		if err := fixture.db.Exec(
			`INSERT INTO media_processing_jobs (id, org_id, asset_id, version_id, status, next_attempt_at)
			 VALUES (?, ?, ?, ?, 'queued', now())`,
			jobID, fixture.orgID, fixture.assetID, versionID,
		).Error; err != nil {
			t.Fatalf("seed extra job: %v", err)
		}

		payload, err := media.Marshal(
			event.Envelope{EventID: outboxID, OrgID: fixture.orgID, OccurredAt: time.Now().UTC()},
			media.Payload{AssetID: fixture.assetID, UploadID: uploadID, VersionID: versionID, JobID: jobID},
		)
		if err != nil {
			t.Fatalf("marshal extra payload: %v", err)
		}
		if err := fixture.db.Exec(
			`INSERT INTO media_job_outbox (id, job_id, event_type, schema_version, payload, status, next_attempt_at)
			 VALUES (?, ?, ?, 1, ?::jsonb, 'pending', now())`,
			outboxID, jobID, media.EventType, string(payload),
		).Error; err != nil {
			t.Fatalf("seed extra outbox row: %v", err)
		}
	}
}

// seedCompletedActiveVersion gives the asset an already-active media version,
// so a replacement has something it could destroy.
func (fixture *mediaJobFixture) seedCompletedActiveVersion(t *testing.T) string {
	t.Helper()

	uploadID, versionID := uuid.NewString(), uuid.NewString()
	rawKey := "raw/" + fixture.orgID + "/" + fixture.assetID + "/" + uploadID + "/original.png"

	if err := fixture.db.Exec(
		`INSERT INTO media_upload_sessions
		 (id, org_id, asset_id, requested_by, idempotency_key, request_fingerprint, state,
		  original_filename, declared_content_type, file_extension, expected_size_bytes,
		  declared_checksum_sha256, raw_object_key, credential_expires_at, session_expires_at, committed_at)
		 VALUES (?, ?, ?, ?, ?, decode(repeat('2a', 32), 'hex'), 'committed', 'prior.png', 'image/png', 'png', 1024,
		         decode(repeat('2a', 32), 'hex'), ?, now() + interval '1 hour', now() + interval '24 hours', now())`,
		uploadID, fixture.orgID, fixture.assetID, fixture.userID, uuid.NewString(), rawKey,
	).Error; err != nil {
		t.Fatalf("seed prior session: %v", err)
	}
	if err := fixture.db.Exec(
		`INSERT INTO asset_media_versions
		 (id, org_id, asset_id, upload_id, requested_by, status, raw_object_key, declared_content_type, original_size_bytes, completed_at, activated_at)
		 VALUES (?, ?, ?, ?, ?, 'completed', ?, 'image/png', 1024, now(), now())`,
		versionID, fixture.orgID, fixture.assetID, uploadID, fixture.userID, rawKey,
	).Error; err != nil {
		t.Fatalf("seed prior version: %v", err)
	}
	if err := fixture.db.Exec(
		"UPDATE metadata_items SET active_media_version_id = ? WHERE id = ?", versionID, fixture.assetID,
	).Error; err != nil {
		t.Fatalf("activate prior version: %v", err)
	}
	return versionID
}

func (fixture *mediaJobFixture) jobRow() domain.MediaProcessingJob {
	fixture.t.Helper()
	var job domain.MediaProcessingJob
	if err := fixture.db.Raw("SELECT * FROM media_processing_jobs WHERE id = ?", fixture.jobID).Scan(&job).Error; err != nil {
		fixture.t.Fatalf("read job: %v", err)
	}
	return job
}

func TestClaimJobTakesTheLeaseAndCountsTheAttempt(t *testing.T) {
	fixture := newMediaJobFixture(t)
	store := fixture.store()

	job, lease, err := store.ClaimJob(fixture.ctx, fixture.jobID, "worker-a")
	if err != nil {
		t.Fatalf("ClaimJob: %v", err)
	}

	if job.Status != domain.ProcessingJobProcessing {
		t.Errorf("status = %q, want processing", job.Status)
	}
	if job.AttemptCount != 1 {
		t.Errorf("attemptCount = %d, want 1", job.AttemptCount)
	}
	if lease.Owner != "worker-a" || lease.ExpiresAt.IsZero() {
		t.Errorf("lease = %+v, want an owned lease with an expiry", lease)
	}
	if job.StartedAt == nil {
		t.Error("startedAt must be stamped on the first claim")
	}
}

func TestClaimJobRefusesAJobAnotherWorkerHolds(t *testing.T) {
	fixture := newMediaJobFixture(t)
	store := fixture.store()

	if _, _, err := store.ClaimJob(fixture.ctx, fixture.jobID, "worker-a"); err != nil {
		t.Fatalf("first claim: %v", err)
	}

	_, _, err := store.ClaimJob(fixture.ctx, fixture.jobID, "worker-b")

	if !errors.Is(err, repository.ErrJobLeased) {
		t.Fatalf("error = %v, want %v", err, repository.ErrJobLeased)
	}
	if attempts := fixture.jobRow().AttemptCount; attempts != 1 {
		t.Errorf("attemptCount = %d, want the refused claim not to count", attempts)
	}
}

// The crashed-worker case: a job left in 'processing' with an expired lease is
// recoverable by the next delivery, without waiting for the reconciliation
// sweep.
func TestClaimJobRecoversAnExpiredLease(t *testing.T) {
	fixture := newMediaJobFixture(t)
	store := fixture.store()

	if _, _, err := store.ClaimJob(fixture.ctx, fixture.jobID, "worker-a"); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if err := fixture.db.Exec(
		"UPDATE media_processing_jobs SET lease_expires_at = now() - interval '1 second' WHERE id = ?", fixture.jobID,
	).Error; err != nil {
		t.Fatalf("expire the lease: %v", err)
	}

	job, lease, err := store.ClaimJob(fixture.ctx, fixture.jobID, "worker-b")
	if err != nil {
		t.Fatalf("recovering an expired lease: %v", err)
	}

	if lease.Owner != "worker-b" {
		t.Errorf("lease owner = %q, want worker-b", lease.Owner)
	}
	if job.AttemptCount != 2 {
		t.Errorf("attemptCount = %d, want the recovery to count as a started execution", job.AttemptCount)
	}
}

func TestClaimJobRefusesTerminalIsolatedAndExhaustedJobs(t *testing.T) {
	cases := map[string]struct {
		setup string
		want  error
	}{
		"completed": {"UPDATE media_processing_jobs SET status = 'completed', completed_at = now() WHERE id = ?", repository.ErrJobTerminal},
		"failed":    {"UPDATE media_processing_jobs SET status = 'failed', failed_at = now() WHERE id = ?", repository.ErrJobTerminal},
		"isolated": {
			"UPDATE media_processing_jobs SET status = 'failed', last_error_code = 'MEDIA_NOTIFICATION_ISOLATED', notification_isolated_at = now() WHERE id = ?",
			repository.ErrJobIsolated,
		},
		"exhausted": {"UPDATE media_processing_jobs SET attempt_count = 3 WHERE id = ?", repository.ErrJobExhausted},
		"not due":   {"UPDATE media_processing_jobs SET next_attempt_at = now() + interval '1 hour' WHERE id = ?", repository.ErrJobNotDue},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			fixture := newMediaJobFixture(t)
			if err := fixture.db.Exec(testCase.setup, fixture.jobID).Error; err != nil {
				t.Fatalf("setup: %v", err)
			}

			_, _, err := fixture.store().ClaimJob(fixture.ctx, fixture.jobID, "worker-a")

			if !errors.Is(err, testCase.want) {
				t.Errorf("error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestClaimJobReportsAMissingJob(t *testing.T) {
	fixture := newMediaJobFixture(t)

	_, _, err := fixture.store().ClaimJob(fixture.ctx, uuid.NewString(), "worker-a")

	if !errors.Is(err, repository.ErrJobNotFound) {
		t.Errorf("error = %v, want %v", err, repository.ErrJobNotFound)
	}
}

func TestRenewLeaseExtendsAHeldLease(t *testing.T) {
	fixture := newMediaJobFixture(t)
	store := fixture.store()

	_, lease, err := store.ClaimJob(fixture.ctx, fixture.jobID, "worker-a")
	if err != nil {
		t.Fatalf("ClaimJob: %v", err)
	}

	renewed, err := store.RenewLease(fixture.ctx, fixture.jobID, lease)
	if err != nil {
		t.Fatalf("RenewLease: %v", err)
	}

	if !renewed.ExpiresAt.After(lease.ExpiresAt) {
		t.Errorf("renewed expiry %s does not advance on %s", renewed.ExpiresAt, lease.ExpiresAt)
	}
	if renewed.Owner != "worker-a" {
		t.Errorf("renewed owner = %q, want worker-a", renewed.Owner)
	}
}

// Matching the expiry as well as the owner is what makes a stale claim
// unrenewable after another worker has taken the job over.
func TestRenewLeaseFailsAfterTheJobIsTakenOver(t *testing.T) {
	fixture := newMediaJobFixture(t)
	store := fixture.store()

	_, stale, err := store.ClaimJob(fixture.ctx, fixture.jobID, "worker-a")
	if err != nil {
		t.Fatalf("ClaimJob: %v", err)
	}
	if err := fixture.db.Exec(
		"UPDATE media_processing_jobs SET lease_expires_at = now() - interval '1 second' WHERE id = ?", fixture.jobID,
	).Error; err != nil {
		t.Fatalf("expire the lease: %v", err)
	}
	if _, _, err := store.ClaimJob(fixture.ctx, fixture.jobID, "worker-b"); err != nil {
		t.Fatalf("takeover claim: %v", err)
	}

	_, err = store.RenewLease(fixture.ctx, fixture.jobID, stale)

	if !errors.Is(err, repository.ErrLeaseNotHeld) {
		t.Errorf("error = %v, want %v", err, repository.ErrLeaseNotHeld)
	}
}

func TestRenewLeaseFailsForANonOwner(t *testing.T) {
	fixture := newMediaJobFixture(t)
	store := fixture.store()

	_, lease, err := store.ClaimJob(fixture.ctx, fixture.jobID, "worker-a")
	if err != nil {
		t.Fatalf("ClaimJob: %v", err)
	}

	impersonated := domain.JobLease{Owner: "worker-b", ExpiresAt: lease.ExpiresAt}
	if _, err := store.RenewLease(fixture.ctx, fixture.jobID, impersonated); !errors.Is(err, repository.ErrLeaseNotHeld) {
		t.Errorf("error = %v, want %v", err, repository.ErrLeaseNotHeld)
	}
}

func TestReleaseLeaseReturnsTheJobToTheQueueWithoutTerminalState(t *testing.T) {
	fixture := newMediaJobFixture(t)
	store := fixture.store()

	_, lease, err := store.ClaimJob(fixture.ctx, fixture.jobID, "worker-a")
	if err != nil {
		t.Fatalf("ClaimJob: %v", err)
	}

	released, err := store.ReleaseLease(fixture.ctx, fixture.jobID, lease)
	if err != nil {
		t.Fatalf("ReleaseLease: %v", err)
	}
	if !released {
		t.Fatal("releasing a held lease must apply")
	}

	job := fixture.jobRow()
	if job.Status != domain.ProcessingJobQueued {
		t.Errorf("status = %q, want queued", job.Status)
	}
	if job.CompletedAt != nil || job.FailedAt != nil {
		t.Error("releasing a lease must not write terminal state")
	}
	if job.AttemptCount != 1 {
		t.Errorf("attemptCount = %d, want the attempt to remain counted", job.AttemptCount)
	}
}

func TestReleaseLeaseDoesNothingForAStaleLease(t *testing.T) {
	fixture := newMediaJobFixture(t)
	store := fixture.store()

	_, stale, err := store.ClaimJob(fixture.ctx, fixture.jobID, "worker-a")
	if err != nil {
		t.Fatalf("ClaimJob: %v", err)
	}
	if err := fixture.db.Exec(
		"UPDATE media_processing_jobs SET lease_expires_at = now() - interval '1 second' WHERE id = ?", fixture.jobID,
	).Error; err != nil {
		t.Fatalf("expire the lease: %v", err)
	}
	if _, _, err := store.ClaimJob(fixture.ctx, fixture.jobID, "worker-b"); err != nil {
		t.Fatalf("takeover claim: %v", err)
	}

	released, err := store.ReleaseLease(fixture.ctx, fixture.jobID, stale)
	if err != nil {
		t.Fatalf("ReleaseLease: %v", err)
	}
	if released {
		t.Error("a superseded worker must not be able to move the job")
	}
	if status := fixture.jobRow().Status; status != domain.ProcessingJobProcessing {
		t.Errorf("status = %q, want the new owner's processing state", status)
	}
}

// A worker holding only a job row cannot find the bytes: the raw key lives on
// the version and the admitted checksum on the session. Loading them together
// is what lets the executor verify content identity before it decodes anything.
func TestLoadProcessingSourceGathersEverythingTheExecutorNeeds(t *testing.T) {
	fixture := newMediaJobFixture(t)

	source, err := fixture.sourceStore().LoadProcessingSource(fixture.ctx, fixture.versionID)
	if err != nil {
		t.Fatalf("LoadProcessingSource: %v", err)
	}

	expectedKey := "raw/" + fixture.orgID + "/" + fixture.assetID + "/" + fixture.uploadID + "/original.png"
	if source.RawObjectKey.String() != expectedKey {
		t.Errorf("rawObjectKey = %q, want %q", source.RawObjectKey, expectedKey)
	}
	if source.DeclaredContentType != domain.MediaContentTypePNG {
		t.Errorf("declaredContentType = %q, want image/png", source.DeclaredContentType)
	}
	if want := bytes.Repeat([]byte{0x2a}, 32); !bytes.Equal(source.AdmittedSHA256, want) {
		t.Errorf("admittedSha256 = %x, want %x", source.AdmittedSHA256, want)
	}
	if source.OrgID != fixture.orgID || source.AssetID != fixture.assetID || source.VersionID != fixture.versionID {
		t.Errorf("source identity = %+v, want the fixture's org/asset/version", source)
	}
}

func TestLoadProcessingSourceReportsAMissingVersion(t *testing.T) {
	fixture := newMediaJobFixture(t)

	_, err := fixture.sourceStore().LoadProcessingSource(fixture.ctx, uuid.NewString())

	if !errors.Is(err, repository.ErrVersionNotFound) {
		t.Fatalf("error = %v, want %v", err, repository.ErrVersionNotFound)
	}
}

func (fixture *mediaJobFixture) sourceStore() interface {
	LoadProcessingSource(context.Context, string) (repository.MediaProcessingSource, error)
} {
	return repository.NewMediaJobStore(
		fixture.db,
		domain.MediaLeasePolicy{RenewalInterval: testJobLeaseRenewal, Expiry: testJobLeaseExpiry},
		testMaxAttempts,
	)
}
