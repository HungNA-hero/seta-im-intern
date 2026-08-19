package usecase

import (
	"context"
	"errors"
	"fmt"

	"seta-im-intern/go-asset-core/internal/domain"
)

// LifecycleObjectStore is deliberately small: retention purge needs only the
// idempotent delete half of object storage, never client presigning or upload.
type LifecycleObjectStore interface {
	Delete(ctx context.Context, key domain.ObjectKey) error
}

type LifecyclePurger struct {
	store   domain.LifecyclePurgeRepository
	objects LifecycleObjectStore
}

func NewLifecyclePurger(store domain.LifecyclePurgeRepository, objects LifecycleObjectStore) *LifecyclePurger {
	return &LifecyclePurger{store: store, objects: objects}
}

// Process drains one claimed PURGE job. It uses small DB transactions around
// one metadata Asset or one folder leaf; storage calls never hold a DB lock.
func (purger *LifecyclePurger) Process(ctx context.Context, jobID, workerID string) error {
	for {
		asset, err := purger.store.NextLifecyclePurgeAsset(ctx, jobID, workerID)
		if err != nil {
			return err
		}
		if asset == nil {
			done, err := purger.store.FinalizeLifecyclePurgeJob(ctx, jobID, workerID)
			if err != nil || done {
				return err
			}
			continue
		}

		for _, key := range asset.ObjectKeys {
			err := purger.objects.Delete(ctx, domain.ObjectKey(key))
			if err != nil && !errors.Is(err, domain.ErrObjectNotFound) {
				return fmt.Errorf("delete purge object %s for asset %s: %w", key, asset.AssetID, err)
			}
		}
		if err := purger.store.MarkLifecyclePurgeObjectsDeleted(ctx, asset.JobID, workerID, asset.AssetID, asset.ObjectKeys); err != nil {
			return err
		}
		if err := purger.store.FinalizeLifecyclePurgeAsset(ctx, asset.JobID, workerID, asset.AssetID); err != nil {
			return err
		}
	}
}
