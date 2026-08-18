package repository_test

import (
	"bytes"
	"context"
	"errors"
	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/repository"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMediaRepository_SupersededWorkerCannotStealTheActivePointer(t *testing.T) {
	fixture := newMediaRepositoryFixture(t)
	asset := fixture.assets[0]

	original := fixture.accept(asset, 10)
	fixture.promote(original)

	superseded := fixture.accept(asset, 20)
	store := fixture.jobStore()
	_, staleLease, err := store.ClaimJob(fixture.ctx, superseded.jobID, "worker-slow")
	if err != nil {
		t.Fatalf("claim the superseded job: %v", err)
	}

	applied, err := store.FailVersion(fixture.ctx, repository.MediaFailure{
		JobID: superseded.jobID, VersionID: superseded.versionID, OrgID: fixture.orgID,
		AssetID: asset, ErrorCode: "PROCESSING_TIMEOUT",
	}, staleLease)
	if err != nil || !applied {
		t.Fatalf("fail the superseded candidate: applied=%v err=%v", applied, err)
	}
	current := fixture.accept(asset, 30)

	applied, err = store.CompleteAndPromote(fixture.ctx, repository.MediaCompletion{
		JobID: superseded.jobID, VersionID: superseded.versionID, OrgID: fixture.orgID,
		AssetID: asset, DetectedContentType: domain.MediaContentTypePNG,
		SourceWidth: 64, SourceHeight: 64, SourceSHA256: bytes.Repeat([]byte{0x11}, 32),
		Outputs: []domain.MediaOutput{
			testOutput(domain.MediaOutputThumbnail, superseded.versionID),
			testOutput(domain.MediaOutputWeb, superseded.versionID),
		},
	}, staleLease)
	if err != nil {
		t.Fatalf("late promotion: %v", err)
	}
	if applied {
		t.Error("a superseded worker must not promote")
	}
	active, pending := fixture.pointers(asset)
	if active == nil || *active != original.versionID {
		t.Errorf("active = %v, want the original still serving", active)
	}
	if pending == nil || *pending != current.versionID {
		t.Errorf("pending = %v, want the current candidate untouched", pending)
	}
}

func (fixture *mediaRepositoryFixture) abandon(session domain.MediaUploadSession) {
	fixture.t.Helper()
	if err := fixture.db.Exec(
		"UPDATE media_upload_sessions SET session_expires_at = ?, credential_expires_at = ? WHERE id = ?",
		fixture.now.Add(-time.Minute), fixture.now.Add(-time.Minute), session.ID,
	).Error; err != nil {
		fixture.t.Fatalf("abandon session: %v", err)
	}
}

func (fixture *mediaRepositoryFixture) sessionState(uploadID string) domain.UploadSessionState {
	fixture.t.Helper()
	var state domain.UploadSessionState
	if err := fixture.db.Raw("SELECT state FROM media_upload_sessions WHERE id = ?", uploadID).Scan(&state).Error; err != nil {
		fixture.t.Fatalf("read session state: %v", err)
	}
	return state
}

func (fixture *mediaRepositoryFixture) usage() domain.OrganizationMediaUsage {
	fixture.t.Helper()
	var usage domain.OrganizationMediaUsage
	if err := fixture.db.Take(&usage, "org_id = ?", fixture.orgID).Error; err != nil {
		fixture.t.Fatalf("read quota ledger: %v", err)
	}
	return usage
}

type candidate struct {
	uploadID  string
	versionID string
	jobID     string
	rawKey    string
}

func (fixture *mediaRepositoryFixture) accept(asset string, size int64) candidate {
	fixture.t.Helper()

	uploadID := uuid.NewString()
	session, _, err := fixture.reserve(fixture.request(asset, uuid.NewString(), size), uploadID, 1_000_000)
	if err != nil {
		fixture.t.Fatalf("reserve replacement session: %v", err)
	}
	accepted, _, err := fixture.repo.CommitUpload(
		fixture.ctx,
		domain.CommitUploadRequest{OrgID: fixture.orgID, AssetID: asset, UploadID: uploadID, RequestedBy: fixture.userID},
		domain.ObjectAttributes{
			SizeBytes:      size,
			ContentType:    string(domain.MediaContentTypePNG),
			ChecksumSHA256: bytes.Repeat([]byte{0x2a}, domain.ChecksumByteLength),
		},
		repository.CommitUploadIDs{VersionID: uuid.NewString(), JobID: uuid.NewString(), OutboxID: uuid.NewString()},
	)
	if err != nil {
		fixture.t.Fatalf("commit replacement: %v", err)
	}
	return candidate{uploadID: uploadID, versionID: accepted.VersionID, jobID: accepted.JobID, rawKey: session.RawObjectKey}
}

func (fixture *mediaRepositoryFixture) jobStore() interface {
	ClaimJob(context.Context, string, string) (domain.MediaProcessingJob, domain.JobLease, error)
	CompleteAndPromote(context.Context, repository.MediaCompletion, domain.JobLease) (bool, error)
	FailVersion(context.Context, repository.MediaFailure, domain.JobLease) (bool, error)
} {
	fixture.t.Helper()
	return repository.NewMediaJobStore(
		fixture.db,
		domain.MediaLeasePolicy{RenewalInterval: testJobLeaseRenewal, Expiry: testJobLeaseExpiry},
		testRetryPolicy(),
	)
}

func (fixture *mediaRepositoryFixture) pointers(asset string) (active, pending *string) {
	fixture.t.Helper()
	var row struct {
		ActiveMediaVersionID  *string
		PendingMediaVersionID *string
	}
	if err := fixture.db.Raw(
		"SELECT active_media_version_id, pending_media_version_id FROM metadata_items WHERE id = ?", asset,
	).Scan(&row).Error; err != nil {
		fixture.t.Fatalf("read pointers: %v", err)
	}
	return row.ActiveMediaVersionID, row.PendingMediaVersionID
}

func (fixture *mediaRepositoryFixture) promote(promoted candidate) {
	fixture.t.Helper()

	store := fixture.jobStore()
	_, lease, err := store.ClaimJob(fixture.ctx, promoted.jobID, "worker-"+promoted.jobID[:8])
	if err != nil {
		fixture.t.Fatalf("claim job: %v", err)
	}
	applied, err := store.CompleteAndPromote(fixture.ctx, repository.MediaCompletion{
		JobID: promoted.jobID, VersionID: promoted.versionID, OrgID: fixture.orgID,
		AssetID: fixture.assets[0], DetectedContentType: domain.MediaContentTypePNG,
		SourceWidth: 64, SourceHeight: 64, SourceSHA256: bytes.Repeat([]byte{0x11}, 32),
		Outputs: []domain.MediaOutput{
			testOutput(domain.MediaOutputThumbnail, promoted.versionID),
			testOutput(domain.MediaOutputWeb, promoted.versionID),
		},
	}, lease)
	if err != nil || !applied {
		fixture.t.Fatalf("promote: applied=%v err=%v", applied, err)
	}
}

func (fixture *mediaRepositoryFixture) fail(failed candidate) {
	fixture.t.Helper()

	store := fixture.jobStore()
	_, lease, err := store.ClaimJob(fixture.ctx, failed.jobID, "worker-"+failed.jobID[:8])
	if err != nil {
		fixture.t.Fatalf("claim job: %v", err)
	}
	applied, err := store.FailVersion(fixture.ctx, repository.MediaFailure{
		JobID: failed.jobID, VersionID: failed.versionID, OrgID: fixture.orgID,
		AssetID: fixture.assets[0], ErrorCode: "INVALID_IMAGE",
	}, lease)
	if err != nil || !applied {
		fixture.t.Fatalf("fail version: applied=%v err=%v", applied, err)
	}
}

func (fixture *mediaRepositoryFixture) versionRow(versionID string) domain.AssetMediaVersion {
	fixture.t.Helper()
	var version domain.AssetMediaVersion
	if err := fixture.db.Take(&version, "id = ?", versionID).Error; err != nil {
		fixture.t.Fatalf("read version %s: %v", versionID, err)
	}
	return version
}

func TestMediaRepository_FailedReplacementLeavesTheActiveVersionServing(t *testing.T) {
	fixture := newMediaRepositoryFixture(t)
	asset := fixture.assets[0]

	original := fixture.accept(asset, 10)
	fixture.promote(original)
	if active, _ := fixture.pointers(asset); active == nil || *active != original.versionID {
		t.Fatalf("active = %v, want the first version promoted", active)
	}

	replacement := fixture.accept(asset, 20)
	active, pending := fixture.pointers(asset)
	if active == nil || *active != original.versionID {
		t.Errorf("active during processing = %v, want the original still serving", active)
	}
	if pending == nil || *pending != replacement.versionID {
		t.Errorf("pending during processing = %v, want the candidate", pending)
	}
	if *active == *pending {
		t.Error("active and pending must never name the same version")
	}

	fixture.fail(replacement)

	active, pending = fixture.pointers(asset)
	if active == nil || *active != original.versionID {
		t.Errorf("active after failure = %v, want the original untouched", active)
	}
	if pending != nil {
		t.Errorf("pending after failure = %v, want it cleared", pending)
	}
	preserved := fixture.versionRow(original.versionID)
	if preserved.Status != domain.MediaVersionCompleted || preserved.RawObjectKey != original.rawKey {
		t.Errorf("original version = %+v, want it completed at %s", preserved, original.rawKey)
	}

	succeeding := fixture.accept(asset, 30)
	fixture.promote(succeeding)

	active, pending = fixture.pointers(asset)
	if active == nil || *active != succeeding.versionID {
		t.Errorf("active after success = %v, want the new version", active)
	}
	if pending != nil {
		t.Errorf("pending after success = %v, want it cleared", pending)
	}
	if kept := fixture.versionRow(original.versionID); kept.RawObjectKey != original.rawKey {
		t.Errorf("superseded raw original = %q, want it retained at %q", kept.RawObjectKey, original.rawKey)
	}
	if failed := fixture.versionRow(replacement.versionID); failed.Status != domain.MediaVersionFailed {
		t.Errorf("failed candidate status = %q, want failed", failed.Status)
	}
}

func TestMediaRepository_AbandonedSessionDoesNotBlockReplacement(t *testing.T) {
	fixture := newMediaRepositoryFixture(t)
	asset := fixture.assets[0]

	abandoned, _, err := fixture.reserve(fixture.request(asset, uuid.NewString(), 4), uuid.NewString(), 100)
	if err != nil {
		t.Fatalf("reserve abandoned session: %v", err)
	}
	fixture.abandon(abandoned)

	replacement, replayed, err := fixture.reserve(fixture.request(asset, uuid.NewString(), 6), uuid.NewString(), 100)
	if err != nil {
		t.Fatalf("replacement after an abandoned session: %v", err)
	}
	if replayed {
		t.Error("a fresh idempotency key must not replay")
	}
	if replacement.State != domain.UploadSessionCreated {
		t.Errorf("replacement state = %q, want created", replacement.State)
	}
}

func TestMediaRepository_ReclaimingAnAbandonedSessionReleasesItsReservation(t *testing.T) {
	fixture := newMediaRepositoryFixture(t)
	asset := fixture.assets[0]

	abandoned, _, err := fixture.reserve(fixture.request(asset, uuid.NewString(), 4), uuid.NewString(), 10)
	if err != nil {
		t.Fatalf("reserve abandoned session: %v", err)
	}
	fixture.abandon(abandoned)

	if _, _, err := fixture.reserve(fixture.request(asset, uuid.NewString(), 8), uuid.NewString(), 10); err != nil {
		t.Fatalf("replacement reusing the reclaimed quota: %v", err)
	}

	if usage := fixture.usage(); usage.ReservedRawBytes != 8 {
		t.Errorf("reservedRawBytes = %d, want only the live session's 8", usage.ReservedRawBytes)
	}
	if state := fixture.sessionState(abandoned.ID); state != domain.UploadSessionExpired {
		t.Errorf("abandoned session state = %q, want expired", state)
	}
}

func TestMediaRepository_ReclaimedSessionCannotCommitLate(t *testing.T) {
	fixture := newMediaRepositoryFixture(t)
	asset := fixture.assets[0]

	abandoned, _, err := fixture.reserve(fixture.request(asset, uuid.NewString(), 4), uuid.NewString(), 100)
	if err != nil {
		t.Fatalf("reserve abandoned session: %v", err)
	}
	fixture.abandon(abandoned)
	if _, _, err := fixture.reserve(fixture.request(asset, uuid.NewString(), 6), uuid.NewString(), 100); err != nil {
		t.Fatalf("replacement: %v", err)
	}

	_, _, err = fixture.repo.CommitUpload(
		fixture.ctx,
		domain.CommitUploadRequest{OrgID: fixture.orgID, AssetID: asset, UploadID: abandoned.ID, RequestedBy: fixture.userID},
		domain.ObjectAttributes{
			SizeBytes:      4,
			ContentType:    string(domain.MediaContentTypePNG),
			ChecksumSHA256: bytes.Repeat([]byte{0x2a}, domain.ChecksumByteLength),
		},
		repository.CommitUploadIDs{VersionID: uuid.NewString(), JobID: uuid.NewString(), OutboxID: uuid.NewString()},
	)

	if !errors.Is(err, repository.ErrUploadStateConflict) {
		t.Fatalf("error = %v, want %v", err, repository.ErrUploadStateConflict)
	}
}
