package usecase_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/repository"
	"seta-im-intern/go-asset-core/internal/usecase"
)

const testHardDeadline = 90 * time.Millisecond

// stubJobStore answers a claim once and then renews until told to revoke.
type stubJobStore struct {
	mutex        sync.Mutex
	claimErr     error
	job          domain.MediaProcessingJob
	lease        domain.JobLease
	revokeAfter  int
	renewals     int
	released     bool
	releaseCalls int
	releasedWith domain.JobLease
}

func newStubJobStore() *stubJobStore {
	return &stubJobStore{
		job:         domain.MediaProcessingJob{ID: "job-1", AssetID: "asset-1", VersionID: "version-1", AttemptCount: 1},
		lease:       domain.JobLease{Owner: "worker-a", ExpiresAt: time.Now().Add(testLeaseExpiry)},
		revokeAfter: -1,
		released:    true,
	}
}

func (store *stubJobStore) ClaimJob(context.Context, string, string) (domain.MediaProcessingJob, domain.JobLease, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if store.claimErr != nil {
		return domain.MediaProcessingJob{}, domain.JobLease{}, store.claimErr
	}
	return store.job, store.lease, nil
}

func (store *stubJobStore) RenewLease(_ context.Context, _ string, held domain.JobLease) (domain.JobLease, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	store.renewals++
	if store.revokeAfter >= 0 && store.renewals > store.revokeAfter {
		return domain.JobLease{}, repository.ErrLeaseNotHeld
	}
	return domain.JobLease{Owner: held.Owner, ExpiresAt: held.ExpiresAt.Add(testLeaseExpiry)}, nil
}

func (store *stubJobStore) ReleaseLease(_ context.Context, _ string, held domain.JobLease) (bool, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	store.releaseCalls++
	store.releasedWith = held
	return store.released, nil
}

func (store *stubJobStore) renewalCount() int {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	return store.renewals
}

func (store *stubJobStore) releasedLease() domain.JobLease {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	return store.releasedWith
}

func (executor *stubExecutor) currentLease() domain.JobLease {
	executor.mutex.Lock()
	defer executor.mutex.Unlock()
	return executor.sawLease
}

// stubExecutor records what it was handed and how it was stopped.
type stubExecutor struct {
	mutex        sync.Mutex
	runFor       time.Duration
	result       error
	calls        int
	sawJob       domain.MediaProcessingJob
	sawLease     domain.JobLease
	claimedLease domain.JobLease
	stoppedBy    error
	sawDeadline  bool
}

func (executor *stubExecutor) Execute(ctx context.Context, job domain.MediaProcessingJob, lease *usecase.HeldLease) error {
	executor.mutex.Lock()
	executor.calls++
	executor.sawJob = job
	executor.claimedLease = lease.Current()
	_, hasDeadline := ctx.Deadline()
	executor.sawDeadline = hasDeadline
	runFor := executor.runFor
	result := executor.result
	executor.mutex.Unlock()

	if runFor > 0 {
		select {
		case <-ctx.Done():
			executor.mutex.Lock()
			executor.stoppedBy = ctx.Err()
			executor.sawLease = lease.Current()
			executor.mutex.Unlock()
			return ctx.Err()
		case <-time.After(runFor):
		}
	}

	// A real executor reads the lease when it writes, which is at the end of its
	// work, not at the start.
	executor.mutex.Lock()
	executor.sawLease = lease.Current()
	executor.mutex.Unlock()
	return result
}

func (executor *stubExecutor) callCount() int {
	executor.mutex.Lock()
	defer executor.mutex.Unlock()
	return executor.calls
}

func newTestWorker(store *stubJobStore, executor *stubExecutor) *usecase.MediaWorker {
	keeper := usecase.NewLeaseKeeper(store, testLeasePolicy(), quietLogger())
	return usecase.NewMediaWorker(store, keeper, executor, "worker-a", testHardDeadline, quietLogger())
}

func TestRunJobExecutesTheClaimedJobUnderItsLease(t *testing.T) {
	store, executor := newStubJobStore(), &stubExecutor{}

	if err := newTestWorker(store, executor).RunJob(context.Background(), "job-1"); err != nil {
		t.Fatalf("RunJob: %v", err)
	}

	if executor.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls)
	}
	if executor.sawJob.ID != "job-1" {
		t.Errorf("executor saw job %q, want job-1", executor.sawJob.ID)
	}
	if executor.claimedLease.Owner != "worker-a" {
		t.Errorf("executor saw lease owner %q, want worker-a", executor.claimedLease.Owner)
	}
	if !executor.sawDeadline {
		t.Error("the executor must run under the claimed-job deadline")
	}
}

// The point of the whole seam: a worker that lost its lease reports loss even
// when the executor finished happily, so no terminal state is written on the
// strength of work another worker has already taken over.
func TestRunJobReportsLeaseLossEvenWhenTheExecutorSucceeds(t *testing.T) {
	store := newStubJobStore()
	store.revokeAfter = 0
	executor := &stubExecutor{runFor: 4 * testRenewalInterval}

	err := newTestWorker(store, executor).RunJob(context.Background(), "job-1")

	if !errors.Is(err, usecase.ErrLeaseLost) {
		t.Fatalf("error = %v, want %v", err, usecase.ErrLeaseLost)
	}
}

func TestRunJobStopsTheExecutorWhenTheLeaseIsLost(t *testing.T) {
	store := newStubJobStore()
	store.revokeAfter = 0
	executor := &stubExecutor{runFor: time.Minute}

	start := time.Now()
	err := newTestWorker(store, executor).RunJob(context.Background(), "job-1")

	if !errors.Is(err, usecase.ErrLeaseLost) {
		t.Fatalf("error = %v, want %v", err, usecase.ErrLeaseLost)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("the executor ran for %s after the lease was lost", elapsed)
	}
	executor.mutex.Lock()
	defer executor.mutex.Unlock()
	if executor.stoppedBy == nil {
		t.Error("losing the lease must cancel the executor's context")
	}
}

func TestRunJobBoundsExecutionByTheClaimedJobDeadline(t *testing.T) {
	store, executor := newStubJobStore(), &stubExecutor{runFor: time.Minute}

	start := time.Now()
	err := newTestWorker(store, executor).RunJob(context.Background(), "job-1")

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want %v", err, context.DeadlineExceeded)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("execution ran %s past its deadline", elapsed)
	}
}

func TestRunJobSettlesFinishedWorkWithoutExecuting(t *testing.T) {
	settled := map[string]error{
		"terminal":  repository.ErrJobTerminal,
		"isolated":  repository.ErrJobIsolated,
		"exhausted": fmt.Errorf("%w: job-1 used 3 of 3", repository.ErrJobExhausted),
	}

	for name, claimErr := range settled {
		t.Run(name, func(t *testing.T) {
			store, executor := newStubJobStore(), &stubExecutor{}
			store.claimErr = claimErr

			err := newTestWorker(store, executor).RunJob(context.Background(), "job-1")

			if !errors.Is(err, usecase.ErrJobSettled) {
				t.Fatalf("error = %v, want %v", err, usecase.ErrJobSettled)
			}
			if executor.callCount() != 0 {
				t.Error("settled work must not be executed")
			}
		})
	}
}

// Contention is not settlement: the job still needs running, so the
// notification must come back rather than be acknowledged.
func TestRunJobReportsContentionAsUnavailable(t *testing.T) {
	for name, claimErr := range map[string]error{
		"leased elsewhere": repository.ErrJobLeased,
		"not due yet":      repository.ErrJobNotDue,
	} {
		t.Run(name, func(t *testing.T) {
			store, executor := newStubJobStore(), &stubExecutor{}
			store.claimErr = claimErr

			err := newTestWorker(store, executor).RunJob(context.Background(), "job-1")

			if !errors.Is(err, usecase.ErrJobUnavailable) {
				t.Fatalf("error = %v, want %v", err, usecase.ErrJobUnavailable)
			}
			if errors.Is(err, usecase.ErrJobSettled) {
				t.Error("contention must never be reported as settled work")
			}
			if executor.callCount() != 0 {
				t.Error("an unclaimed job must not be executed")
			}
		})
	}
}

func TestRunJobPropagatesAnUnrecognizedClaimFailure(t *testing.T) {
	store, executor := newStubJobStore(), &stubExecutor{}
	store.claimErr = errors.New("connection refused")

	err := newTestWorker(store, executor).RunJob(context.Background(), "job-1")

	if errors.Is(err, usecase.ErrJobSettled) {
		t.Fatalf("a database outage must not settle a job: %v", err)
	}
	if err == nil {
		t.Fatal("an unrecognized claim failure must surface")
	}
}

func TestRunJobReturnsTheExecutorFailure(t *testing.T) {
	processingFailed := errors.New("rendition failed")
	store, executor := newStubJobStore(), &stubExecutor{result: processingFailed}

	err := newTestWorker(store, executor).RunJob(context.Background(), "job-1")

	if !errors.Is(err, processingFailed) {
		t.Fatalf("error = %v, want %v", err, processingFailed)
	}
}

// A failed attempt goes back to the queue immediately rather than sitting in
// 'processing' until its lease expires.
func TestRunJobReleasesTheLeaseAfterAFailedAttempt(t *testing.T) {
	store, executor := newStubJobStore(), &stubExecutor{result: errors.New("rendition failed")}

	_ = newTestWorker(store, executor).RunJob(context.Background(), "job-1")

	store.mutex.Lock()
	defer store.mutex.Unlock()
	if store.releaseCalls != 1 {
		t.Errorf("release calls = %d, want 1", store.releaseCalls)
	}
}

// A successful attempt is the executor's to settle, so the worker must not
// hand the job back and undo the terminal state the executor just wrote.
func TestRunJobDoesNotReleaseTheLeaseAfterSuccess(t *testing.T) {
	store, executor := newStubJobStore(), &stubExecutor{}

	if err := newTestWorker(store, executor).RunJob(context.Background(), "job-1"); err != nil {
		t.Fatalf("RunJob: %v", err)
	}

	store.mutex.Lock()
	defer store.mutex.Unlock()
	if store.releaseCalls != 0 {
		t.Errorf("release calls = %d, want 0", store.releaseCalls)
	}
}

// Losing the lease means abandoning silently: even the release is another
// worker's business now.
func TestRunJobDoesNotReleaseALostLease(t *testing.T) {
	store := newStubJobStore()
	store.revokeAfter = 0
	executor := &stubExecutor{runFor: 4 * testRenewalInterval}

	if err := newTestWorker(store, executor).RunJob(context.Background(), "job-1"); !errors.Is(err, usecase.ErrLeaseLost) {
		t.Fatalf("error = %v, want %v", err, usecase.ErrLeaseLost)
	}

	store.mutex.Lock()
	defer store.mutex.Unlock()
	if store.releaseCalls != 0 {
		t.Errorf("release calls = %d, want 0", store.releaseCalls)
	}
}

// Every terminal write matches the lease expiry exactly, so a job that outlives
// one renewal interval must present the renewed lease, not the claimed one. A
// worker that hands over a stale lease can neither promote, fail, nor requeue
// its own job: it finishes with the version stuck pending and nothing left to
// replay.
func TestRunJobPresentsTheRenewedLeaseToTheExecutor(t *testing.T) {
	store := newStubJobStore()
	executor := &stubExecutor{runFor: 3 * testRenewalInterval}
	claimed := store.lease

	if err := newTestWorker(store, executor).RunJob(context.Background(), "job-1"); err != nil {
		t.Fatalf("RunJob: %v", err)
	}

	if store.renewalCount() == 0 {
		t.Fatal("the job did not outlive a renewal interval; the test proves nothing")
	}
	if executor.currentLease().ExpiresAt.Equal(claimed.ExpiresAt) {
		t.Errorf("executor saw the claimed expiry %s after %d renewals", claimed.ExpiresAt, store.renewalCount())
	}
}

// The same staleness breaks the requeue path, which is what leaves a failed
// attempt sitting in 'processing' until its lease expires.
func TestRunJobReleasesWithTheRenewedLease(t *testing.T) {
	store := newStubJobStore()
	executor := &stubExecutor{runFor: 3 * testRenewalInterval, result: errors.New("storage unavailable")}
	claimed := store.lease

	_ = newTestWorker(store, executor).RunJob(context.Background(), "job-1")

	if store.renewalCount() == 0 {
		t.Fatal("the job did not outlive a renewal interval; the test proves nothing")
	}
	if store.releasedLease().ExpiresAt.Equal(claimed.ExpiresAt) {
		t.Errorf("ReleaseLease was given the claimed expiry %s, which matches no row", claimed.ExpiresAt)
	}
}
