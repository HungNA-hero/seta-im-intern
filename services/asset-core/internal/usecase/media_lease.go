package usecase

import (
	"context"
	"errors"
	"log/slog"
	"seta-im-intern/go-asset-core/internal/domain"
	"sync"
	"time"
)

var ErrLeaseLost = errors.New("media job lease was lost")

type LeaseRenewer interface {
	RenewLease(ctx context.Context, jobID string, held domain.JobLease) (domain.JobLease, error)
}

type LeaseKeeper struct {
	renewer LeaseRenewer
	policy  domain.MediaLeasePolicy
	logger  *slog.Logger
}

func NewLeaseKeeper(renewer LeaseRenewer, policy domain.MediaLeasePolicy, logger *slog.Logger) *LeaseKeeper {
	if logger == nil {
		logger = slog.Default()
	}
	return &LeaseKeeper{renewer: renewer, policy: policy, logger: logger}
}

// HeldLease is the lease as it stands right now. Renewal replaces the expiry
// the database will accept, and every terminal write matches that expiry
// exactly, so a caller must read the lease at the moment it writes rather than
// keep the one it was handed at claim time.
type HeldLease struct {
	mutex   sync.Mutex
	current domain.JobLease
}

func NewHeldLease(claimed domain.JobLease) *HeldLease {
	return &HeldLease{current: claimed}
}

func (lease *HeldLease) Current() domain.JobLease {
	lease.mutex.Lock()
	defer lease.mutex.Unlock()
	return lease.current
}

func (lease *HeldLease) replace(renewed domain.JobLease) {
	lease.mutex.Lock()
	defer lease.mutex.Unlock()
	lease.current = renewed
}

func (keeper *LeaseKeeper) Hold(ctx context.Context, jobID string, claimed domain.JobLease) (context.Context, *HeldLease, func()) {
	held, cancel := context.WithCancelCause(ctx)
	stop := make(chan struct{})
	stopped := make(chan struct{})
	lease := NewHeldLease(claimed)

	go keeper.renewUntilLost(ctx, jobID, lease, cancel, stop, stopped)

	var once sync.Once
	return held, lease, func() {
		once.Do(func() { close(stop) })
		<-stopped
		cancel(nil)
	}
}

func (keeper *LeaseKeeper) renewUntilLost(
	ctx context.Context,
	jobID string,
	lease *HeldLease,
	cancel context.CancelCauseFunc,
	stop <-chan struct{},
	stopped chan struct{},
) {
	defer close(stopped)

	ticker := time.NewTicker(keeper.policy.RenewalInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		renewed, err := keeper.renewer.RenewLease(ctx, jobID, lease.Current())
		if err == nil {
			lease.replace(renewed)
			continue
		}
		if ctx.Err() != nil {
			return
		}

		keeper.logger.Warn(
			"media job lease renewal failed; abandoning the job without terminal state",
			"jobId", jobID,
			"leaseOwner", lease.Current().Owner,
			"error", err.Error(),
		)
		cancel(ErrLeaseLost)
		return
	}
}

func LeaseWasLost(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), ErrLeaseLost)
}
