package usecase_test

import (
	"context"
	"errors"
	"testing"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/usecase"
)

type lifecyclePurgeStoreFake struct {
	work      *domain.LifecyclePurgeAsset
	marked    []string
	finalized []string
	done      bool
}

func (fake *lifecyclePurgeStoreFake) NextLifecyclePurgeAsset(context.Context, string, string) (*domain.LifecyclePurgeAsset, error) {
	work := fake.work
	fake.work = nil
	return work, nil
}
func (fake *lifecyclePurgeStoreFake) MarkLifecyclePurgeObjectsDeleted(_ context.Context, _ string, _ string, _ string, keys []string) error {
	fake.marked = append(fake.marked, keys...)
	return nil
}
func (fake *lifecyclePurgeStoreFake) FinalizeLifecyclePurgeAsset(_ context.Context, _ string, _ string, assetID string) error {
	fake.finalized = append(fake.finalized, assetID)
	return nil
}
func (fake *lifecyclePurgeStoreFake) FinalizeLifecyclePurgeJob(context.Context, string, string) (bool, error) {
	return fake.done, nil
}

type lifecycleObjectStoreFake struct {
	deleted []string
	errFor  map[string]error
}

func (fake *lifecycleObjectStoreFake) Delete(_ context.Context, key domain.ObjectKey) error {
	fake.deleted = append(fake.deleted, key.String())
	return fake.errFor[key.String()]
}

func TestLifecyclePurger_DeletesObjectsBeforeFinalizingDatabaseAsset(t *testing.T) {
	store := &lifecyclePurgeStoreFake{
		work: &domain.LifecyclePurgeAsset{JobID: "purge-job", AssetID: "asset", ObjectKeys: []string{"raw", "thumbnail", "web"}},
		done: true,
	}
	objects := &lifecycleObjectStoreFake{errFor: map[string]error{"thumbnail": domain.ErrObjectNotFound}}
	if err := usecase.NewLifecyclePurger(store, objects).Process(context.Background(), "purge-job", "worker-a"); err != nil {
		t.Fatalf("process lifecycle purge: %v", err)
	}
	if len(store.marked) != 3 || len(store.finalized) != 1 {
		t.Fatalf("marked=%v finalized=%v; object removal must finish before DB teardown", store.marked, store.finalized)
	}
}

func TestLifecyclePurger_DoesNotFinalizeWhenStorageDeleteFails(t *testing.T) {
	store := &lifecyclePurgeStoreFake{work: &domain.LifecyclePurgeAsset{JobID: "purge-job", AssetID: "asset", ObjectKeys: []string{"raw"}}}
	objects := &lifecycleObjectStoreFake{errFor: map[string]error{"raw": errors.New("minio unavailable")}}
	if err := usecase.NewLifecyclePurger(store, objects).Process(context.Background(), "purge-job", "worker-a"); err == nil {
		t.Fatal("expected storage error")
	}
	if len(store.marked) != 0 || len(store.finalized) != 0 {
		t.Fatalf("marked=%v finalized=%v; DB must remain untouched after a storage failure", store.marked, store.finalized)
	}
}
