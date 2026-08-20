package media

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

type ReconciliationStore interface {
	ReconcileStaleDispatches(ctx context.Context, batchSize int, staleAfter time.Duration) (int, error)
}

type ReconcilerOptions struct {
	Interval   time.Duration
	BatchSize  int
	StaleAfter time.Duration
	Logger     *slog.Logger
}

var (
	ErrReconciliationStoreRequired = errors.New("media reconciliation store is required")
	ErrInvalidReconcilerOptions    = errors.New("invalid media reconciliation options")
)

type Reconciler struct {
	store   ReconciliationStore
	options ReconcilerOptions
}

func NewReconciler(store ReconciliationStore, options ReconcilerOptions) (*Reconciler, error) {
	if store == nil {
		return nil, ErrReconciliationStoreRequired
	}
	if options.Interval <= 0 || options.BatchSize <= 0 || options.StaleAfter <= 0 {
		return nil, ErrInvalidReconcilerOptions
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	return &Reconciler{store: store, options: options}, nil
}

func (reconciler *Reconciler) RunOnce(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return reconciler.store.ReconcileStaleDispatches(
		ctx,
		reconciler.options.BatchSize,
		reconciler.options.StaleAfter,
	)
}

func (reconciler *Reconciler) Run(ctx context.Context) {
	ticker := time.NewTicker(reconciler.options.Interval)
	defer ticker.Stop()

	for {
		inserted, err := reconciler.RunOnce(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			reconciler.options.Logger.Error(
				"media reconciliation sweep failed",
				"errorCode", "MEDIA_RECONCILIATION_FAILED",
			)
		} else if inserted > 0 {
			reconciler.options.Logger.Debug("media dispatches reconciled", "inserted", inserted)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
