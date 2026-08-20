package usecase

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/observability"
	"seta-im-intern/go-asset-core/internal/repository"
)

type PurgeableObjectStore interface {
	ExpireUploadSessions(ctx context.Context, limit int) (int, error)
	ClaimPurgeableMediaObjects(ctx context.Context, quarantine time.Duration, limit int) ([]repository.PurgeableMediaObjects, error)
	MarkMediaObjectsPurged(ctx context.Context, purged repository.PurgeableMediaObjects) error
}

type MediaPurgeOptions struct {
	Quarantine time.Duration
	BatchSize  int
	Logger     *slog.Logger
}

type mediaOperationalSnapshotStore interface {
	GetMediaOperationalSnapshot(context.Context, time.Duration) (repository.MediaOperationalSnapshot, error)
}

// MediaObjectPurger reclaims storage that nothing references: raw objects from
// uploads the client walked away from, and the raw and partial derivatives of
// versions that failed.
//
// Storage is deleted before the purge is recorded. A crash between the two
// repeats an idempotent delete on the next sweep, whereas recording first would
// strand the object permanently — nothing would ever offer it again.
type MediaObjectPurger struct {
	objects MediaObjectStore
	store   PurgeableObjectStore
	options MediaPurgeOptions
}

func NewMediaObjectPurger(store PurgeableObjectStore, objects MediaObjectStore, options MediaPurgeOptions) *MediaObjectPurger {
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if options.BatchSize <= 0 {
		options.BatchSize = 50
	}
	return &MediaObjectPurger{store: store, objects: objects, options: options}
}

// PurgeOnce reclaims at most one batch and reports how many sessions it
// settled. A batch that cannot be fully deleted is left unmarked so the next
// sweep retries it.
func (purger *MediaObjectPurger) PurgeOnce(ctx context.Context) (int, error) {
	defer purger.refreshOperationalMetrics(ctx)
	// The two halves are independent: sessions that could not be expired are
	// reported and the sweep still reclaims whatever storage is already eligible.
	expired, err := purger.store.ExpireUploadSessions(ctx, purger.options.BatchSize)
	if err != nil {
		purger.options.Logger.Warn("could not expire every abandoned upload session", "error", err.Error())
	}
	if expired > 0 {
		observability.RecordMediaAbandonedSessions(expired)
		purger.options.Logger.Info("expired abandoned upload sessions", "sessions", expired)
	}

	batches, err := purger.store.ClaimPurgeableMediaObjects(ctx, purger.options.Quarantine, purger.options.BatchSize)
	if err != nil {
		return 0, err
	}

	purged := 0
	for _, batch := range batches {
		if err := purger.purgeBatch(ctx, batch); err != nil {
			observability.RecordMediaFailure("storage")
			purger.options.Logger.Warn(
				"could not reclaim a quarantined media object",
				"uploadId", batch.UploadID,
				"error", err.Error(),
			)
			continue
		}
		purged++
	}
	return purged, nil
}

func (purger *MediaObjectPurger) refreshOperationalMetrics(ctx context.Context) {
	reader, ok := purger.store.(mediaOperationalSnapshotStore)
	if !ok || ctx.Err() != nil {
		return
	}
	snapshot, err := reader.GetMediaOperationalSnapshot(ctx, purger.options.Quarantine)
	if err != nil {
		purger.options.Logger.Warn("could not refresh media operational metrics", "errorCode", "MEDIA_METRICS_REFRESH_FAILED")
		return
	}
	observability.SetMediaBacklogs(observability.MediaBacklogs{
		QueueOldestAge:          snapshot.QueueOldestAge,
		OutboxOldestAge:         snapshot.OutboxOldestAge,
		ReconciliationOldestAge: snapshot.ReconciliationOldestAge,
		Cleanup:                 snapshot.CleanupBacklog,
		Quarantine:              snapshot.QuarantineBacklog,
		QuarantineOldestAge:     snapshot.QuarantineOldestAge,
	})
	quota := make([]observability.MediaQuotaHeadroom, 0, len(snapshot.Quota))
	for _, observation := range snapshot.Quota {
		quota = append(quota, observability.MediaQuotaHeadroom{
			OrganizationID: observation.OrganizationID,
			ConsumedBytes:  observation.ConsumedBytes,
			QuotaBytes:     observation.QuotaBytes,
		})
	}
	observability.SetMediaQuotaHeadroom(quota, purger.options.Logger)
}

func (purger *MediaObjectPurger) purgeBatch(ctx context.Context, batch repository.PurgeableMediaObjects) error {
	for _, key := range batch.ProcessedObjectKeys {
		if err := purger.deleteObject(ctx, domain.ObjectKey(key)); err != nil {
			return err
		}
	}
	if batch.RawObjectKey != "" {
		if err := purger.deleteObject(ctx, domain.ObjectKey(batch.RawObjectKey)); err != nil {
			return err
		}
	}
	return purger.store.MarkMediaObjectsPurged(ctx, batch)
}

// deleteObject treats an already-absent object as done. The sweep runs after
// crashes and retries, so the common case for a second attempt is that the
// first one succeeded.
func (purger *MediaObjectPurger) deleteObject(ctx context.Context, key domain.ObjectKey) error {
	err := purger.objects.Delete(ctx, key)
	if err == nil || errors.Is(err, domain.ErrObjectNotFound) {
		return nil
	}
	return err
}

// PurgeUntilStopped runs the sweep on a fixed interval until the context ends.
func (purger *MediaObjectPurger) PurgeUntilStopped(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		purged, err := purger.PurgeOnce(ctx)
		if err != nil && ctx.Err() == nil {
			purger.options.Logger.Error("media object sweep failed", "error", err.Error())
		}
		if purged > 0 {
			purger.options.Logger.Info("reclaimed quarantined media objects", "sessions", purged)
		}
	}
}
