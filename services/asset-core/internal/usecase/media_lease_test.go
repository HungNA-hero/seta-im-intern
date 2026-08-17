package usecase_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/usecase"
)

var errLeaseRevoked = errors.New("lease revoked")

const (
	testRenewalInterval = 5 * time.Millisecond
	testLeaseExpiry     = 40 * time.Millisecond
)

func testLeasePolicy() domain.MediaLeasePolicy {
	return domain.MediaLeasePolicy{RenewalInterval: testRenewalInterval, Expiry: testLeaseExpiry}
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// recordingRenewer answers a fixed number of renewals before failing, and
// remembers the lease it was asked to renew each time.
type recordingRenewer struct {
	mutex     sync.Mutex
	successes int
	seen      []domain.JobLease
	failWith  error
	clock     time.Time
}

func newRecordingRenewer(successes int, failWith error) *recordingRenewer {
	return &recordingRenewer{
		successes: successes,
		failWith:  failWith,
		clock:     time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC),
	}
}

func (renewer *recordingRenewer) RenewLease(_ context.Context, _ string, held domain.JobLease) (domain.JobLease, error) {
	renewer.mutex.Lock()
	defer renewer.mutex.Unlock()

	renewer.seen = append(renewer.seen, held)
	if len(renewer.seen) > renewer.successes {
		return domain.JobLease{}, renewer.failWith
	}
	renewer.clock = renewer.clock.Add(testLeaseExpiry)
	return domain.JobLease{Owner: held.Owner, ExpiresAt: renewer.clock}, nil
}

func (renewer *recordingRenewer) observed() []domain.JobLease {
	renewer.mutex.Lock()
	defer renewer.mutex.Unlock()
	return append([]domain.JobLease(nil), renewer.seen...)
}

func startingLease() domain.JobLease {
	return domain.JobLease{
		Owner:     "worker-a",
		ExpiresAt: time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC),
	}
}

// Long transformation is the case renewal exists for: the work outlives the
// lease expiry, and the context must stay live throughout.
func TestHoldKeepsTheContextLiveAcrossLongWork(t *testing.T) {
	renewer := newRecordingRenewer(1000, errLeaseRevoked)
	keeper := usecase.NewLeaseKeeper(renewer, testLeasePolicy(), quietLogger())

	held, _, release := keeper.Hold(context.Background(), "job-1", startingLease())
	defer release()

	time.Sleep(testLeaseExpiry + 5*testRenewalInterval)

	if err := held.Err(); err != nil {
		t.Fatalf("held context ended during healthy renewal: %v", err)
	}
	if usecase.LeaseWasLost(held) {
		t.Error("a healthy lease must not report as lost")
	}
	if len(renewer.observed()) < 2 {
		t.Errorf("renewals = %d, want the lease renewed repeatedly", len(renewer.observed()))
	}
}

// Each renewal must present the expiry the previous one returned. Renewing
// against the original expiry forever would let a stale claim survive.
func TestHoldRenewsAgainstTheMostRecentlyReturnedLease(t *testing.T) {
	renewer := newRecordingRenewer(1000, errLeaseRevoked)
	keeper := usecase.NewLeaseKeeper(renewer, testLeasePolicy(), quietLogger())

	held, _, release := keeper.Hold(context.Background(), "job-1", startingLease())
	defer release()

	time.Sleep(6 * testRenewalInterval)
	_ = held

	observed := renewer.observed()
	if len(observed) < 3 {
		t.Fatalf("renewals = %d, want at least 3", len(observed))
	}
	for index := 1; index < len(observed); index++ {
		if !observed[index].ExpiresAt.After(observed[index-1].ExpiresAt) {
			t.Fatalf("renewal %d presented expiry %s, which does not advance on %s",
				index, observed[index].ExpiresAt, observed[index-1].ExpiresAt)
		}
	}
}

func TestHoldCancelsWithLeaseLostWhenRenewalMatchesNoRow(t *testing.T) {
	renewer := newRecordingRenewer(1, errLeaseRevoked)
	keeper := usecase.NewLeaseKeeper(renewer, testLeasePolicy(), quietLogger())

	held, _, release := keeper.Hold(context.Background(), "job-1", startingLease())
	defer release()

	select {
	case <-held.Done():
	case <-time.After(time.Second):
		t.Fatal("a revoked lease must cancel the held context")
	}

	if !usecase.LeaseWasLost(held) {
		t.Errorf("cause = %v, want %v", context.Cause(held), usecase.ErrLeaseLost)
	}
}

// Shutdown is not lease loss. Reporting it as loss would make an orderly stop
// look like a stolen job in every log and metric.
func TestHoldDoesNotReportLeaseLossWhenTheCallerStops(t *testing.T) {
	renewer := newRecordingRenewer(1000, errLeaseRevoked)
	keeper := usecase.NewLeaseKeeper(renewer, testLeasePolicy(), quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	held, _, release := keeper.Hold(ctx, "job-1", startingLease())
	defer release()

	cancel()
	<-held.Done()

	if usecase.LeaseWasLost(held) {
		t.Errorf("cause = %v, want the caller's cancellation", context.Cause(held))
	}
}

// Release must return even when nothing else has gone wrong: the renewer
// watches the caller's context, so it cannot be stopped by cancelling the held
// context and needs its own signal.
func TestReleaseReturnsWhileRenewalIsHealthy(t *testing.T) {
	renewer := newRecordingRenewer(1000, errLeaseRevoked)
	keeper := usecase.NewLeaseKeeper(renewer, testLeasePolicy(), quietLogger())

	_, _, release := keeper.Hold(context.Background(), "job-1", startingLease())

	returned := make(chan struct{})
	go func() {
		release()
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("release blocked; the renewer never stopped")
	}
}

func TestReleaseIsSafeToCallTwice(t *testing.T) {
	renewer := newRecordingRenewer(1000, errLeaseRevoked)
	keeper := usecase.NewLeaseKeeper(renewer, testLeasePolicy(), quietLogger())

	_, _, release := keeper.Hold(context.Background(), "job-1", startingLease())

	release()
	release()
}

func TestReleaseStopsRenewing(t *testing.T) {
	renewer := newRecordingRenewer(1000, errLeaseRevoked)
	keeper := usecase.NewLeaseKeeper(renewer, testLeasePolicy(), quietLogger())

	_, _, release := keeper.Hold(context.Background(), "job-1", startingLease())
	time.Sleep(3 * testRenewalInterval)
	release()

	afterRelease := len(renewer.observed())
	time.Sleep(4 * testRenewalInterval)

	if got := len(renewer.observed()); got != afterRelease {
		t.Errorf("renewals continued after release: %d then %d", afterRelease, got)
	}
}
