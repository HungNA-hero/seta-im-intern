// Package media carries the media processing payload on the shared KAN-81
// envelope. The generic fields, their versioning rules, and the
// prohibited-content list belong to internal/eventing/event and are not
// restated here — this package owns the four identifiers media adds.
package media

import (
	"encoding/json"
	"errors"
	"fmt"
	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/eventing/event"

	"github.com/google/uuid"
)

const (
	EventType      = domain.MediaProcessingRequestedEventType
	SchemaVersion  = 1
	Source         = "asset-core"
	AggregateField = "jobId"
	MaxRecordBytes = 2048
)

var (
	ErrMissingIdentifier = errors.New("media event identifier missing")
	ErrInvalidIdentifier = errors.New("media event identifier is not a UUID")
	ErrRecordTooLarge    = errors.New("media event exceeds the contracted size ceiling")
)

type Payload struct {
	AssetID   string `json:"assetId"`
	UploadID  string `json:"uploadId"`
	VersionID string `json:"versionId"`
	JobID     string `json:"jobId"`
}

type record struct {
	event.Envelope
	Payload
}

func Marshal(envelope event.Envelope, payload Payload) ([]byte, error) {
	if envelope.OccurredAt.IsZero() {
		return nil, fmt.Errorf("%w: occurredAt", ErrMissingIdentifier)
	}
	if err := payload.validate(); err != nil {
		return nil, err
	}

	envelope.EventType = EventType
	envelope.SchemaVersion = SchemaVersion
	envelope.Source = Source
	envelope.OccurredAt = envelope.OccurredAt.UTC()

	value, err := json.Marshal(record{Envelope: envelope, Payload: payload})
	if err != nil {
		return nil, err
	}
	if len(value) > MaxRecordBytes {
		return nil, fmt.Errorf("%w: %d bytes", ErrRecordTooLarge, len(value))
	}
	return value, nil
}

func Parse(envelope event.Envelope) (Payload, error) {
	var payload Payload
	if err := json.Unmarshal(envelope.Raw, &payload); err != nil {
		return Payload{}, fmt.Errorf("%w: %w", event.ErrMalformed, err)
	}
	if err := payload.validate(); err != nil {
		return Payload{}, err
	}
	return payload, nil
}

func (payload Payload) validate() error {
	for name, value := range map[string]string{
		"assetId":   payload.AssetID,
		"uploadId":  payload.UploadID,
		"versionId": payload.VersionID,
		"jobId":     payload.JobID,
	} {
		if value == "" {
			return fmt.Errorf("%w: %s", ErrMissingIdentifier, name)
		}
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("%w: %s", ErrInvalidIdentifier, name)
		}
	}
	return nil
}
