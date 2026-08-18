package usecase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/repository"
	"time"
)

var (
	ErrJobSettled     = errors.New("media job needs no further processing")
	ErrJobUnavailable = errors.New("media job cannot be claimed right now")
)

type MediaJobStore interface {
	ClaimJob(ctx context.Context, jobID, owner string) (domain.MediaProcessingJob, domain.JobLease, error)
	RenewLease(ctx context.Context, jobID string, held domain.JobLease) (domain.JobLease, error)
	SettleExecutionFailure(ctx context.Context, job domain.MediaProcessingJob, held domain.JobLease) (bool, error)
}

type MediaJobExecutor interface {
	Execute(ctx context.Context, job domain.MediaProcessingJob, lease *HeldLease) error
}

type MediaWorker struct {
	jobs         MediaJobStore
	leases       *LeaseKeeper
	executor     MediaJobExecutor
	owner        string
	hardDeadline time.Duration
	logger       *slog.Logger
}

func NewMediaWorker(
	jobs MediaJobStore,
	leases *LeaseKeeper,
	executor MediaJobExecutor,
	owner string,
	hardDeadline time.Duration,
	logger *slog.Logger,
) *MediaWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &MediaWorker{
		jobs:         jobs,
		leases:       leases,
		executor:     executor,
		owner:        owner,
		hardDeadline: hardDeadline,
		logger:       logger,
	}
}

func (worker *MediaWorker) RunJob(ctx context.Context, jobID string) error {
	job, lease, err := worker.jobs.ClaimJob(ctx, jobID, worker.owner)
	if err != nil {
		return worker.classifyClaimFailure(jobID, err)
	}

	held, heldLease, release := worker.leases.Hold(ctx, jobID, lease)
	defer release()

	bounded, cancel := context.WithTimeout(held, worker.hardDeadline)
	defer cancel()

	executeErr := worker.executor.Execute(bounded, job, heldLease)

	if LeaseWasLost(held) {
		worker.logger.Warn(
			"abandoning media job without terminal state; the lease was lost",
			"jobId", jobID,
			"leaseOwner", worker.owner,
			"attempt", job.AttemptCount,
		)
		return fmt.Errorf("%w: %s", ErrLeaseLost, jobID)
	}

	if executeErr != nil {
		settled, settleErr := worker.jobs.SettleExecutionFailure(ctx, job, heldLease.Current())
		if settleErr != nil {
			return fmt.Errorf("settling failed media job %s: %w", jobID, settleErr)
		}
		if !settled {
			return fmt.Errorf("%w: %s", ErrLeaseLost, jobID)
		}
		worker.logger.Warn(
			"durably settled a failed media execution",
			"jobId", jobID,
			"attempt", job.AttemptCount,
			"error", executeErr.Error(),
		)
		return nil
	}
	return nil
}

func (worker *MediaWorker) classifyClaimFailure(jobID string, err error) error {
	switch {
	case errors.Is(err, repository.ErrJobTerminal),
		errors.Is(err, repository.ErrJobIsolated):
		worker.logger.Info("media job needs no processing", "jobId", jobID, "reason", err.Error())
		return fmt.Errorf("%w: %s", ErrJobSettled, jobID)

	case errors.Is(err, repository.ErrJobLeased), errors.Is(err, repository.ErrJobNotDue):
		return fmt.Errorf("%w: %w", ErrJobUnavailable, err)

	default:
		return err
	}
}
