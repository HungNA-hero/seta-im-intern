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
	ReleaseLease(ctx context.Context, jobID string, held domain.JobLease) (bool, error)
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
		worker.returnToQueue(ctx, jobID, heldLease.Current(), executeErr)
	}
	return executeErr
}

func (worker *MediaWorker) returnToQueue(ctx context.Context, jobID string, lease domain.JobLease, cause error) {
	released, err := worker.jobs.ReleaseLease(ctx, jobID, lease)
	if err != nil {
		worker.logger.Error("releasing a failed media job failed", "jobId", jobID, "error", err.Error())
		return
	}
	if !released {
		return
	}
	worker.logger.Warn("returned a failed media job to the queue", "jobId", jobID, "error", cause.Error())
}

func (worker *MediaWorker) classifyClaimFailure(jobID string, err error) error {
	switch {
	case errors.Is(err, repository.ErrJobTerminal),
		errors.Is(err, repository.ErrJobIsolated),
		errors.Is(err, repository.ErrJobExhausted):
		worker.logger.Info("media job needs no processing", "jobId", jobID, "reason", err.Error())
		return fmt.Errorf("%w: %s", ErrJobSettled, jobID)

	case errors.Is(err, repository.ErrJobLeased), errors.Is(err, repository.ErrJobNotDue):
		return fmt.Errorf("%w: %w", ErrJobUnavailable, err)

	default:
		return err
	}
}
