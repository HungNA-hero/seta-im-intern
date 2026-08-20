package usecase_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/usecase"
)

type lifecyclePurgeStoreFake struct {
	work      *domain.LifecyclePurgeAsset
	marked    []string
	finalized []string
	done      bool

	mutex       sync.Mutex
	renewals    int
	renewErr    error
	renewedOnce chan struct{}
	renewOnce   sync.Once
}

func (fake *lifecyclePurgeStoreFake) NextLifecyclePurgeAsset(context.Context, string, string) (*domain.LifecyclePurgeAsset, error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
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

func (fake *lifecyclePurgeStoreFake) RenewLifecyclePurgeLease(_ context.Context, _ string, _ string, expiry time.Time) (time.Time, error) {
	fake.mutex.Lock()
	fake.renewals++
	err := fake.renewErr
	fake.mutex.Unlock()
	if fake.renewedOnce != nil {
		fake.renewOnce.Do(func() { close(fake.renewedOnce) })
	}
	if err != nil {
		return time.Time{}, err
	}
	return expiry.Add(time.Minute), nil
}

func (fake *lifecyclePurgeStoreFake) renewalCount() int {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return fake.renewals
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
	if err := usecase.NewLifecyclePurger(store, objects).Process(context.Background(), "purge-job", "worker-a", time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("process lifecycle purge: %v", err)
	}
	if len(store.marked) != 3 || len(store.finalized) != 1 {
		t.Fatalf("marked=%v finalized=%v; object removal must finish before DB teardown", store.marked, store.finalized)
	}
}

func TestLifecyclePurger_DoesNotFinalizeWhenStorageDeleteFails(t *testing.T) {
	store := &lifecyclePurgeStoreFake{work: &domain.LifecyclePurgeAsset{JobID: "purge-job", AssetID: "asset", ObjectKeys: []string{"raw"}}}
	objects := &lifecycleObjectStoreFake{errFor: map[string]error{"raw": errors.New("minio unavailable")}}
	if err := usecase.NewLifecyclePurger(store, objects).Process(context.Background(), "purge-job", "worker-a", time.Now().Add(time.Minute)); err == nil {
		t.Fatal("expected storage error")
	}
	if len(store.marked) != 0 || len(store.finalized) != 0 {
		t.Fatalf("marked=%v finalized=%v; DB must remain untouched after a storage failure", store.marked, store.finalized)
	}
}

type waitForRenewalObjectStore struct {
	renewed <-chan struct{}
}

func (store waitForRenewalObjectStore) Delete(ctx context.Context, _ domain.ObjectKey) error {
	select {
	case <-store.renewed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type waitForCancellationObjectStore struct{}

func (waitForCancellationObjectStore) Delete(ctx context.Context, _ domain.ObjectKey) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestLifecyclePurger_RenewsLeaseWhileDrainIsStillRunning(t *testing.T) {
	renewed := make(chan struct{})
	store := &lifecyclePurgeStoreFake{
		work:        &domain.LifecyclePurgeAsset{JobID: "purge-job", AssetID: "asset", ObjectKeys: []string{"raw"}},
		done:        true,
		renewedOnce: renewed,
	}
	purger := usecase.NewLifecyclePurgerWithRenewalInterval(store, waitForRenewalObjectStore{renewed: renewed}, time.Millisecond)

	if err := purger.Process(context.Background(), "purge-job", "worker-a", time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("process lifecycle purge with renewal: %v", err)
	}
	if store.renewalCount() == 0 {
		t.Fatal("purger completed long-running work without renewing its lease")
	}
	if len(store.marked) != 1 || len(store.finalized) != 1 {
		t.Fatalf("marked=%v finalized=%v; purge must finish after a healthy renewal", store.marked, store.finalized)
	}
}

func TestLifecyclePurger_StopsWithoutDatabaseTeardownWhenLeaseIsLost(t *testing.T) {
	store := &lifecyclePurgeStoreFake{
		work:     &domain.LifecyclePurgeAsset{JobID: "purge-job", AssetID: "asset", ObjectKeys: []string{"raw"}},
		renewErr: errors.New("lease belongs to another worker"),
	}
	purger := usecase.NewLifecyclePurgerWithRenewalInterval(store, waitForCancellationObjectStore{}, time.Millisecond)

	err := purger.Process(context.Background(), "purge-job", "worker-a", time.Now().Add(time.Minute))
	if !errors.Is(err, usecase.ErrLifecyclePurgeLeaseLost) {
		t.Fatalf("process error = %v, want %v", err, usecase.ErrLifecyclePurgeLeaseLost)
	}
	if len(store.marked) != 0 || len(store.finalized) != 0 {
		t.Fatalf("marked=%v finalized=%v; stale worker must not mutate database state", store.marked, store.finalized)
	}
}
