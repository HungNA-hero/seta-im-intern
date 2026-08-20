package usecase

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"seta-im-intern/go-asset-core/internal/domain"
)

var ErrLifecyclePurgeLeaseLost = errors.New("lifecycle purge job lease was lost")

const defaultLifecyclePurgeLeaseRenewalInterval = domain.FolderDeletionLeaseDuration / 3

// LifecycleObjectStore is deliberately small: retention purge needs only the
// idempotent delete half of object storage, never client presigning or upload.
type LifecycleObjectStore interface {
	Delete(ctx context.Context, key domain.ObjectKey) error
}

type LifecyclePurger struct {
	store        domain.LifecyclePurgeRepository
	objects      LifecycleObjectStore
	renewalEvery time.Duration
}

func NewLifecyclePurger(store domain.LifecyclePurgeRepository, objects LifecycleObjectStore) *LifecyclePurger {
	return NewLifecyclePurgerWithRenewalInterval(store, objects, defaultLifecyclePurgeLeaseRenewalInterval)
}

// NewLifecyclePurgerWithRenewalInterval exists so the lease boundary can be
// tested without waiting for the production renewal interval.
func NewLifecyclePurgerWithRenewalInterval(
	store domain.LifecyclePurgeRepository,
	objects LifecycleObjectStore,
	renewalEvery time.Duration,
) *LifecyclePurger {
	if renewalEvery <= 0 {
		renewalEvery = defaultLifecyclePurgeLeaseRenewalInterval
	}
	return &LifecyclePurger{store: store, objects: objects, renewalEvery: renewalEvery}
}

// Process drains one claimed PURGE job. It uses small DB transactions around
// one metadata Asset or one folder leaf; storage calls never hold a DB lock.
// A background keeper renews the claim while those bounded operations run.
func (purger *LifecyclePurger) Process(ctx context.Context, jobID, workerID string, claimedExpiry time.Time) error {
	processingCtx, release := purger.holdLease(ctx, jobID, workerID, claimedExpiry)
	defer release()

	for {
		if err := lifecyclePurgeContextError(processingCtx); err != nil {
			return err
		}
		asset, err := purger.store.NextLifecyclePurgeAsset(processingCtx, jobID, workerID)
		if err != nil {
			return lifecyclePurgeResult(processingCtx, err)
		}
		if asset == nil {
			done, err := purger.store.FinalizeLifecyclePurgeJob(processingCtx, jobID, workerID)
			if err != nil || done {
				return lifecyclePurgeResult(processingCtx, err)
			}
			continue
		}

		for _, key := range asset.ObjectKeys {
			err := purger.objects.Delete(processingCtx, domain.ObjectKey(key))
			if err != nil && !errors.Is(err, domain.ErrObjectNotFound) {
				return lifecyclePurgeResult(processingCtx, fmt.Errorf("delete purge object %s for asset %s: %w", key, asset.AssetID, err))
			}
		}
		if err := purger.store.MarkLifecyclePurgeObjectsDeleted(processingCtx, asset.JobID, workerID, asset.AssetID, asset.ObjectKeys); err != nil {
			return lifecyclePurgeResult(processingCtx, err)
		}
		if err := purger.store.FinalizeLifecyclePurgeAsset(processingCtx, asset.JobID, workerID, asset.AssetID); err != nil {
			return lifecyclePurgeResult(processingCtx, err)
		}
	}
}

func (purger *LifecyclePurger) holdLease(
	ctx context.Context,
	jobID, workerID string,
	claimedExpiry time.Time,
) (context.Context, func()) {
	heldCtx, cancel := context.WithCancelCause(ctx)
	stop := make(chan struct{})
	stopped := make(chan struct{})

	go func() {
		defer close(stopped)
		ticker := time.NewTicker(purger.renewalEvery)
		defer ticker.Stop()
		expectedExpiry := claimedExpiry
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
			}

			renewedExpiry, err := purger.store.RenewLifecyclePurgeLease(ctx, jobID, workerID, expectedExpiry)
			if err != nil {
				cancel(ErrLifecyclePurgeLeaseLost)
				return
			}
			expectedExpiry = renewedExpiry
		}
	}()

	var once sync.Once
	return heldCtx, func() {
		once.Do(func() { close(stop) })
		<-stopped
		cancel(nil)
	}
}

func lifecyclePurgeContextError(ctx context.Context) error {
	if errors.Is(context.Cause(ctx), ErrLifecyclePurgeLeaseLost) {
		return ErrLifecyclePurgeLeaseLost
	}
	return ctx.Err()
}

func lifecyclePurgeResult(ctx context.Context, err error) error {
	if errors.Is(context.Cause(ctx), ErrLifecyclePurgeLeaseLost) {
		return ErrLifecyclePurgeLeaseLost
	}
	return err
}
