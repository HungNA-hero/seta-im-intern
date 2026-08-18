package usecase_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/repository"
	"seta-im-intern/go-asset-core/internal/usecase"
)

type fakePurgeStore struct {
	batches     []repository.PurgeableMediaObjects
	expireErr   error
	claimErr    error
	marked      []string
	markErr     error
	expireCalls int
	claimCalls  int
}

func (store *fakePurgeStore) ExpireUploadSessions(_ context.Context, _ int) (int, error) {
	store.expireCalls++
	return 0, store.expireErr
}

func (store *fakePurgeStore) ClaimPurgeableMediaObjects(_ context.Context, _ time.Duration, _ int) ([]repository.PurgeableMediaObjects, error) {
	store.claimCalls++
	return store.batches, store.claimErr
}

func (store *fakePurgeStore) MarkMediaObjectsPurged(_ context.Context, purged repository.PurgeableMediaObjects) error {
	if store.markErr != nil {
		return store.markErr
	}
	store.marked = append(store.marked, purged.UploadID)
	return nil
}

func newPurger(store *fakePurgeStore, objects *fakeObjectStore) *usecase.MediaObjectPurger {
	return usecase.NewMediaObjectPurger(store, objects, usecase.MediaPurgeOptions{
		Quarantine: 24 * time.Hour,
		BatchSize:  50,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func purgeableBatch() repository.PurgeableMediaObjects {
	return repository.PurgeableMediaObjects{
		UploadID:            testUploadID,
		VersionID:           testVersionID,
		OrgID:               testOrgID,
		RawObjectKey:        "raw/org/asset/upload/original.png",
		ProcessedObjectKeys: []string{"processed/version/thumbnail-256.png", "processed/version/web-1080.png"},
		StoredBytes:         1024,
	}
}

func TestPurgeOnceExpiresSessionsBeforeClaimingObjects(t *testing.T) {
	store := &fakePurgeStore{}
	objects := &fakeObjectStore{stored: map[string][]byte{}}

	if _, err := newPurger(store, objects).PurgeOnce(context.Background()); err != nil {
		t.Fatalf("PurgeOnce: %v", err)
	}
	if store.expireCalls != 1 {
		t.Errorf("expiration calls = %d, want 1", store.expireCalls)
	}
	if store.claimCalls != 1 {
		t.Errorf("object claim calls = %d, want 1", store.claimCalls)
	}
}

// Expiring sessions and reclaiming objects are independent halves of the sweep.
// A failure in the first must not stop the second: one session the database
// refuses to transition would otherwise block every object reclamation, on
// every sweep, permanently.
func TestPurgeOnceStillReclaimsObjectsWhenSessionExpirationFails(t *testing.T) {
	batch := purgeableBatch()
	store := &fakePurgeStore{
		batches:   []repository.PurgeableMediaObjects{batch},
		expireErr: errors.New("one session could not be expired"),
	}
	objects := &fakeObjectStore{stored: map[string][]byte{}}

	purged, err := newPurger(store, objects).PurgeOnce(context.Background())
	if err != nil {
		t.Fatalf("PurgeOnce: %v", err)
	}

	if store.claimCalls != 1 {
		t.Errorf("object claim calls = %d, want the sweep to continue", store.claimCalls)
	}
	if purged != 1 {
		t.Errorf("purged = %d, want the object batch still reclaimed", purged)
	}
}

func TestPurgeOnceDeletesEveryObjectThenRecordsThePurge(t *testing.T) {
	batch := purgeableBatch()
	store := &fakePurgeStore{batches: []repository.PurgeableMediaObjects{batch}}
	objects := &fakeObjectStore{stored: map[string][]byte{
		batch.RawObjectKey:           []byte("raw"),
		batch.ProcessedObjectKeys[0]: []byte("thumb"),
		batch.ProcessedObjectKeys[1]: []byte("web"),
	}}

	purged, err := newPurger(store, objects).PurgeOnce(context.Background())
	if err != nil {
		t.Fatalf("PurgeOnce: %v", err)
	}

	if purged != 1 {
		t.Errorf("purged = %d, want 1", purged)
	}
	for _, key := range append(batch.ProcessedObjectKeys, batch.RawObjectKey) {
		if !contains(objects.deleted, key) {
			t.Errorf("deleted = %v, want it to include %s", objects.deleted, key)
		}
	}
	if len(store.marked) != 1 || store.marked[0] != batch.UploadID {
		t.Errorf("marked = %v, want the batch recorded once", store.marked)
	}
}

// Recording the purge before the delete would strand the object forever: the
// marker stops it ever being offered again, so the bytes would leak silently.
func TestPurgeOnceLeavesABatchUnmarkedWhenDeletionFails(t *testing.T) {
	batch := purgeableBatch()
	store := &fakePurgeStore{batches: []repository.PurgeableMediaObjects{batch}}
	objects := &fakeObjectStore{
		stored:    map[string][]byte{},
		deleteErr: errors.New("storage unavailable"),
	}

	purged, err := newPurger(store, objects).PurgeOnce(context.Background())
	if err != nil {
		t.Fatalf("PurgeOnce must not fail the sweep for one bad batch: %v", err)
	}

	if purged != 0 {
		t.Errorf("purged = %d, want the failed batch not counted", purged)
	}
	if len(store.marked) != 0 {
		t.Error("a batch whose objects survive must not be recorded as purged")
	}
}

// The sweep runs after crashes and retries, so a second attempt normally finds
// the object already gone. That is completion, not failure.
func TestPurgeOnceTreatsAnAbsentObjectAsAlreadyReclaimed(t *testing.T) {
	batch := purgeableBatch()
	store := &fakePurgeStore{batches: []repository.PurgeableMediaObjects{batch}}
	objects := &fakeObjectStore{stored: map[string][]byte{}, deleteErr: domain.ErrObjectNotFound}

	purged, err := newPurger(store, objects).PurgeOnce(context.Background())
	if err != nil {
		t.Fatalf("PurgeOnce: %v", err)
	}

	if purged != 1 {
		t.Errorf("purged = %d, want an already-deleted object to settle", purged)
	}
	if len(store.marked) != 1 {
		t.Error("an already-reclaimed batch must still be recorded")
	}
}

func TestPurgeOnceContinuesPastOneUnreclaimableBatch(t *testing.T) {
	failing := purgeableBatch()
	failing.UploadID = "upload-failing"
	failing.RawObjectKey = "raw/org/asset/failing/original.png"
	healthy := purgeableBatch()
	healthy.UploadID = "upload-healthy"
	healthy.RawObjectKey = "raw/org/asset/healthy/original.png"
	healthy.ProcessedObjectKeys = nil

	store := &fakePurgeStore{batches: []repository.PurgeableMediaObjects{failing, healthy}}
	objects := &fakeObjectStore{stored: map[string][]byte{}, deleteErrFor: map[string]error{
		failing.RawObjectKey: errors.New("storage unavailable"),
	}}

	purged, err := newPurger(store, objects).PurgeOnce(context.Background())
	if err != nil {
		t.Fatalf("PurgeOnce: %v", err)
	}

	if purged != 1 {
		t.Errorf("purged = %d, want the healthy batch still reclaimed", purged)
	}
	if len(store.marked) != 1 || store.marked[0] != healthy.UploadID {
		t.Errorf("marked = %v, want only the healthy batch", store.marked)
	}
}
