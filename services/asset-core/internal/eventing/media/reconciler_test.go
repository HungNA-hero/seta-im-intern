package media

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type recordingReconciliationStore struct {
	batch      int
	staleAfter time.Duration
	inserted   int
	err        error
}

type notifyingReconciliationStore struct {
	mutex sync.Mutex
	calls chan struct{}
	errs  []error
}

func (store *notifyingReconciliationStore) ReconcileStaleDispatches(
	context.Context,
	int,
	time.Duration,
) (int, error) {
	store.mutex.Lock()
	var err error
	if len(store.errs) > 0 {
		err = store.errs[0]
		store.errs = store.errs[1:]
	}
	store.mutex.Unlock()
	store.calls <- struct{}{}
	return 0, err
}

func (store *recordingReconciliationStore) ReconcileStaleDispatches(
	_ context.Context,
	batchSize int,
	staleAfter time.Duration,
) (int, error) {
	store.batch = batchSize
	store.staleAfter = staleAfter
	return store.inserted, store.err
}

func TestReconcilerRunOnceRepairsOneBoundedStaleBatch(t *testing.T) {
	store := &recordingReconciliationStore{inserted: 7}
	reconciler, err := NewReconciler(store, ReconcilerOptions{
		Interval:   30 * time.Second,
		BatchSize:  50,
		StaleAfter: 60 * time.Second,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewReconciler: %v", err)
	}

	inserted, err := reconciler.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if inserted != 7 || store.batch != 50 || store.staleAfter != 60*time.Second {
		t.Fatalf(
			"reconciliation = inserted %d batch %d stale %s",
			inserted,
			store.batch,
			store.staleAfter,
		)
	}
}

func TestReconcilerRunsImmediatelyRetriesAfterErrorAndStopsWithContext(t *testing.T) {
	store := &notifyingReconciliationStore{
		calls: make(chan struct{}, 4),
		errs:  []error{errors.New("database unavailable")},
	}
	reconciler, err := NewReconciler(store, ReconcilerOptions{
		Interval:   5 * time.Millisecond,
		BatchSize:  50,
		StaleAfter: 60 * time.Second,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewReconciler: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		reconciler.Run(ctx)
		close(done)
	}()

	for call := 1; call <= 2; call++ {
		select {
		case <-store.calls:
		case <-time.After(250 * time.Millisecond):
			t.Fatalf("timed out waiting for reconciliation call %d", call)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("reconciler did not stop after context cancellation")
	}
}
