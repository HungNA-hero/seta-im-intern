package repository_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/repository"
)

func (fixture *mediaJobFixture) promotionStore() interface {
	CompleteAndPromote(context.Context, repository.MediaCompletion, domain.JobLease) (bool, error)
	FailVersion(context.Context, repository.MediaFailure, domain.JobLease) (bool, error)
} {
	return repository.NewMediaJobStore(
		fixture.db,
		domain.MediaLeasePolicy{RenewalInterval: testJobLeaseRenewal, Expiry: testJobLeaseExpiry},
		testRetryPolicy(),
	)
}

func (fixture *mediaJobFixture) completion(outputs ...domain.MediaOutput) repository.MediaCompletion {
	detected := domain.MediaContentTypePNG
	return repository.MediaCompletion{
		JobID:               fixture.jobID,
		VersionID:           fixture.versionID,
		OrgID:               fixture.orgID,
		AssetID:             fixture.assetID,
		DetectedContentType: detected,
		SourceWidth:         64,
		SourceHeight:        64,
		SourceSHA256:        bytes.Repeat([]byte{0x11}, 32),
		Outputs:             outputs,
	}
}

func testOutput(kind domain.MediaOutputKind, versionID string) domain.MediaOutput {
	sizes := map[domain.MediaOutputKind]int{domain.MediaOutputThumbnail: 64, domain.MediaOutputWeb: 512}
	return domain.MediaOutput{
		VersionID:   versionID,
		Kind:        kind,
		ObjectKey:   "processed/" + versionID + "/" + string(kind) + "-" + uuid.NewString() + ".png",
		ContentType: domain.MediaContentTypePNG,
		Width:       sizes[kind],
		Height:      sizes[kind],
		SizeBytes:   1024,
		SHA256:      bytes.Repeat([]byte{0x22}, 32),
	}
}

func (fixture *mediaJobFixture) bothOutputs() []domain.MediaOutput {
	return []domain.MediaOutput{
		testOutput(domain.MediaOutputThumbnail, fixture.versionID),
		testOutput(domain.MediaOutputWeb, fixture.versionID),
	}
}

func (fixture *mediaJobFixture) assetPointers(t *testing.T) (active, pending *string) {
	t.Helper()
	var row struct {
		ActiveMediaVersionID  *string
		PendingMediaVersionID *string
	}
	if err := fixture.db.Raw(
		"SELECT active_media_version_id, pending_media_version_id FROM metadata_items WHERE id = ?", fixture.assetID,
	).Scan(&row).Error; err != nil {
		t.Fatalf("read pointers: %v", err)
	}
	return row.ActiveMediaVersionID, row.PendingMediaVersionID
}

func (fixture *mediaJobFixture) attachPending(t *testing.T) {
	t.Helper()
	if err := fixture.db.Exec(
		"UPDATE metadata_items SET pending_media_version_id = ? WHERE id = ?", fixture.versionID, fixture.assetID,
	).Error; err != nil {
		t.Fatalf("attach pending version: %v", err)
	}
}

func (fixture *mediaJobFixture) claim(t *testing.T, owner string) domain.JobLease {
	t.Helper()
	_, lease, err := fixture.store().ClaimJob(fixture.ctx, fixture.jobID, owner)
	if err != nil {
		t.Fatalf("ClaimJob: %v", err)
	}
	return lease
}

func TestCompleteAndPromoteActivatesTheVersion(t *testing.T) {
	fixture := newMediaJobFixture(t)
	fixture.attachPending(t)
	lease := fixture.claim(t, "worker-a")

	applied, err := fixture.promotionStore().CompleteAndPromote(fixture.ctx, fixture.completion(fixture.bothOutputs()...), lease)
	if err != nil {
		t.Fatalf("CompleteAndPromote: %v", err)
	}
	if !applied {
		t.Fatal("promotion under a held lease must apply")
	}

	active, pending := fixture.assetPointers(t)
	if active == nil || *active != fixture.versionID {
		t.Errorf("active pointer = %v, want the promoted version", active)
	}
	if pending != nil {
		t.Errorf("pending pointer = %v, want it cleared", pending)
	}
	if status := fixture.jobRow().Status; status != domain.ProcessingJobCompleted {
		t.Errorf("job status = %q, want completed", status)
	}
}

// The whole point of the transaction: an incomplete set of derivatives must
// never switch the pointer a reader follows.
func TestCompleteAndPromoteRefusesAnIncompleteOutputSet(t *testing.T) {
	cases := map[string][]domain.MediaOutput{
		"no outputs":      {},
		"thumbnail only":  {testOutput(domain.MediaOutputThumbnail, "")},
		"web only":        {testOutput(domain.MediaOutputWeb, "")},
		"duplicated kind": {testOutput(domain.MediaOutputThumbnail, ""), testOutput(domain.MediaOutputThumbnail, "")},
	}

	for name, outputs := range cases {
		t.Run(name, func(t *testing.T) {
			fixture := newMediaJobFixture(t)
			fixture.attachPending(t)
			lease := fixture.claim(t, "worker-a")

			for index := range outputs {
				outputs[index].VersionID = fixture.versionID
			}

			_, err := fixture.promotionStore().CompleteAndPromote(fixture.ctx, fixture.completion(outputs...), lease)

			if !errors.Is(err, repository.ErrIncompleteOutputSet) {
				t.Fatalf("error = %v, want %v", err, repository.ErrIncompleteOutputSet)
			}
			if active, _ := fixture.assetPointers(t); active != nil {
				t.Errorf("active pointer = %v, want it untouched", active)
			}
			if status := fixture.jobRow().Status; status == domain.ProcessingJobCompleted {
				t.Error("the job must not be completed without both derivatives")
			}
		})
	}
}

// A superseded worker finishing late must not overwrite the active version the
// new owner is working towards.
func TestCompleteAndPromoteRefusesAStaleLease(t *testing.T) {
	fixture := newMediaJobFixture(t)
	fixture.attachPending(t)
	stale := fixture.claim(t, "worker-a")

	if err := fixture.db.Exec(
		"UPDATE media_processing_jobs SET lease_expires_at = clock_timestamp() - interval '1 second' WHERE id = ?", fixture.jobID,
	).Error; err != nil {
		t.Fatalf("expire the lease: %v", err)
	}
	if _, _, err := fixture.store().ClaimJob(fixture.ctx, fixture.jobID, "worker-b"); err != nil {
		t.Fatalf("takeover claim: %v", err)
	}

	applied, err := fixture.promotionStore().CompleteAndPromote(fixture.ctx, fixture.completion(fixture.bothOutputs()...), stale)
	if err != nil {
		t.Fatalf("CompleteAndPromote: %v", err)
	}

	if applied {
		t.Error("a superseded worker must not promote")
	}
	if active, _ := fixture.assetPointers(t); active != nil {
		t.Errorf("active pointer = %v, want it untouched by the stale worker", active)
	}
}

// Redelivery after a successful promotion must be a no-op, not a second
// activation effect.
func TestCompleteAndPromoteIsIdempotentOnRedelivery(t *testing.T) {
	fixture := newMediaJobFixture(t)
	fixture.attachPending(t)
	lease := fixture.claim(t, "worker-a")
	completion := fixture.completion(fixture.bothOutputs()...)

	if _, err := fixture.promotionStore().CompleteAndPromote(fixture.ctx, completion, lease); err != nil {
		t.Fatalf("first promotion: %v", err)
	}

	// The job is terminal now, so a redelivered notification cannot even claim it.
	_, _, err := fixture.store().ClaimJob(fixture.ctx, fixture.jobID, "worker-b")
	if !errors.Is(err, repository.ErrJobTerminal) {
		t.Fatalf("error = %v, want %v", err, repository.ErrJobTerminal)
	}

	active, _ := fixture.assetPointers(t)
	if active == nil || *active != fixture.versionID {
		t.Errorf("active pointer = %v, want it still the promoted version", active)
	}
}

// A failed replacement must leave the prior active rendition exactly as it was.
func TestFailVersionLeavesThePriorActiveVersionIntact(t *testing.T) {
	fixture := newMediaJobFixture(t)
	priorActive := fixture.seedCompletedActiveVersion(t)
	fixture.attachPending(t)
	lease := fixture.claim(t, "worker-a")

	applied, err := fixture.promotionStore().FailVersion(fixture.ctx, repository.MediaFailure{
		JobID:     fixture.jobID,
		VersionID: fixture.versionID,
		OrgID:     fixture.orgID,
		AssetID:   fixture.assetID,
		ErrorCode: "INVALID_IMAGE",
	}, lease)
	if err != nil {
		t.Fatalf("FailVersion: %v", err)
	}
	if !applied {
		t.Fatal("failing a held job must apply")
	}

	active, pending := fixture.assetPointers(t)
	if active == nil || *active != priorActive {
		t.Errorf("active pointer = %v, want the prior version %s preserved", active, priorActive)
	}
	if pending != nil {
		t.Errorf("pending pointer = %v, want the failed candidate cleared", pending)
	}
	if status := fixture.jobRow().Status; status != domain.ProcessingJobFailed {
		t.Errorf("job status = %q, want failed", status)
	}
}
