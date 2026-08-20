package media

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"seta-im-intern/go-asset-core/internal/eventing/consume"
	"seta-im-intern/go-asset-core/internal/observability"
)

const (
	DeadLetterTopic    = "media-processing-dlq.v1"
	MaxDeadLetterBytes = 1024
)

type NotificationIsolationStore interface {
	IsolateNotification(ctx context.Context, record consume.QuarantinedRecord) error
}

type DeadLetterPublisher interface {
	Publish(ctx context.Context, topic, key string, payload []byte) error
}

type DeadLetter struct {
	store     NotificationIsolationStore
	publisher DeadLetterPublisher
	topic     string
}

type deadLetterRecord struct {
	SourceTopic     string `json:"sourceTopic"`
	SourcePartition int    `json:"sourcePartition"`
	SourceOffset    int64  `json:"sourceOffset"`
	QuarantineID    string `json:"quarantineId"`
	EventID         string `json:"eventId,omitempty"`
	JobID           string `json:"jobId,omitempty"`
	ObservedAt      string `json:"observedAt"`
	ReasonCode      string `json:"reasonCode"`
	PayloadSHA256   string `json:"payloadSha256"`
}

func NewDeadLetter(store NotificationIsolationStore, publisher DeadLetterPublisher, topic string) *DeadLetter {
	return &DeadLetter{store: store, publisher: publisher, topic: topic}
}

func (deadLetter *DeadLetter) Isolate(ctx context.Context, record consume.QuarantinedRecord) error {
	eventID := validatedDeadLetterUUID(record.EventID)
	jobID := validatedDeadLetterUUID(record.AggregateID)
	if jobID != "" {
		record.AggregateID = jobID
		if err := deadLetter.store.IsolateNotification(ctx, record); err != nil {
			return fmt.Errorf("persisting media notification isolation: %w", err)
		}
	}

	payload, err := json.Marshal(deadLetterRecord{
		SourceTopic:     record.SourceTopic,
		SourcePartition: record.SourcePartition,
		SourceOffset:    record.SourceOffset,
		QuarantineID:    record.QuarantineID,
		EventID:         eventID,
		JobID:           jobID,
		ObservedAt:      record.ObservedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		ReasonCode:      record.ReasonCode,
		PayloadSHA256:   record.PayloadSHA256,
	})
	if err != nil {
		return fmt.Errorf("encoding media dead-letter record: %w", err)
	}
	if len(payload) > MaxDeadLetterBytes {
		return fmt.Errorf("media dead-letter record exceeds %d bytes", MaxDeadLetterBytes)
	}
	if err := deadLetter.publisher.Publish(ctx, deadLetter.topic, record.QuarantineID, payload); err != nil {
		observability.RecordMediaFailure("quarantine")
		return fmt.Errorf("publishing media dead-letter record: %w", err)
	}
	observability.RecordMediaFailure("quarantine")
	return nil
}

func validatedDeadLetterUUID(value string) string {
	parsed, err := uuid.Parse(value)
	if err != nil {
		return ""
	}
	return parsed.String()
}
