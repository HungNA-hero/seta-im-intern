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

type notificationVerifier interface {
	VerifyNotification(ctx context.Context, orgID string, payload media.Payload) error
}

type notificationEffect struct {
	runner   jobRunner
	verifier notificationVerifier
	logger   *slog.Logger
}

func (effect *notificationEffect) Apply(ctx context.Context, envelope event.Envelope) error {
	payload, parseErr := media.Parse(envelope)
	if parseErr != nil {
		effect.logger.Error(
			"rejecting a media notification with an unusable payload",
			"eventId", envelope.EventID,
		)
		return consume.Poison("INVALID_MEDIA_PAYLOAD")
	}
	if effect.verifier != nil {
		if err := effect.verifier.VerifyNotification(ctx, envelope.OrgID, payload); err != nil {
			switch {
			case errors.Is(err, repository.ErrJobNotFound):
				return consume.Poison("MEDIA_JOB_NOT_FOUND")
			case errors.Is(err, repository.ErrNotificationMismatch):
				return consume.Poison("MEDIA_NOTIFICATION_MISMATCH")
			default:
				return err
			}
		}
	}

	err := effect.runner.RunJob(ctx, payload.JobID)
	switch {
	case err == nil:
		return nil

	case errors.Is(err, usecase.ErrJobSettled):
		return consume.ErrAlreadyApplied

	case errors.Is(err, repository.ErrJobNotFound):
		effect.logger.Error(
			"rejecting a media notification for a job that does not exist",
			"eventId", envelope.EventID,
			"jobId", payload.JobID,
		)
		return consume.Poison("MEDIA_JOB_NOT_FOUND")

	default:
		return err
	}
}
