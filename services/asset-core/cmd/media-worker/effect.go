package main

import (
	"context"
	"errors"
	"log/slog"
	"seta-im-intern/go-asset-core/internal/eventing/consume"
	"seta-im-intern/go-asset-core/internal/eventing/event"
	"seta-im-intern/go-asset-core/internal/eventing/media"
	"seta-im-intern/go-asset-core/internal/repository"
	"seta-im-intern/go-asset-core/internal/usecase"
)

type jobRunner interface {
	RunJob(ctx context.Context, jobID string) error
}

type notificationEffect struct {
	runner jobRunner
	logger *slog.Logger
}

func (effect *notificationEffect) Apply(ctx context.Context, envelope event.Envelope) error {
	payload, parseErr := media.Parse(envelope)
	if parseErr != nil {
		effect.logger.Error(
			"dropping a media notification with an unusable payload",
			"eventId", envelope.EventID,
			"error", parseErr.Error(),
		)
		return consume.ErrAlreadyApplied
	}

	err := effect.runner.RunJob(ctx, payload.JobID)
	switch {
	case err == nil:
		return nil

	case errors.Is(err, usecase.ErrJobSettled):
		return consume.ErrAlreadyApplied

	case errors.Is(err, repository.ErrJobNotFound):
		effect.logger.Error(
			"dropping a media notification for a job that does not exist",
			"eventId", envelope.EventID,
			"jobId", payload.JobID,
		)
		return consume.ErrAlreadyApplied

	default:
		return err
	}
}

type loggingQuarantine struct {
	logger *slog.Logger
}

func (quarantine *loggingQuarantine) Isolate(_ context.Context, quarantined consume.QuarantinedRecord) error {
	quarantine.logger.Error(
		"quarantining a media notification",
		"quarantineId", quarantined.QuarantineID,
		"reasonCode", quarantined.ReasonCode,
		"sourceTopic", quarantined.SourceTopic,
		"sourcePartition", quarantined.SourcePartition,
		"sourceOffset", quarantined.SourceOffset,
		"eventId", quarantined.EventID,
		"jobId", quarantined.AggregateID,
		"payloadSha256", quarantined.PayloadSHA256,
	)
	return nil
}
