package event

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type Envelope struct {
	EventID       string          `json:"eventId"`
	EventType     string          `json:"eventType"`
	SchemaVersion int             `json:"schemaVersion"`
	Source        string          `json:"source"`
	OccurredAt    time.Time       `json:"occurredAt"`
	OrgID         string          `json:"orgId"`
	Traceparent   string          `json:"traceparent,omitempty"`
	Raw           json.RawMessage `json:"-"`
}

var (
	ErrMalformed      = errors.New("envelope is not valid JSON")
	ErrUnknownVersion = errors.New("unknown schema version")
	ErrMissingField   = errors.New("required envelope field missing")
	ErrInvalidField   = errors.New("envelope field has an invalid format")
)

func Parse(value []byte, knownVersions []int) (Envelope, error) {
	var envelope Envelope
	if err := json.Unmarshal(value, &envelope); err != nil {
		return Envelope{}, fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	envelope.Raw = json.RawMessage(value)

	if !isKnownVersion(envelope.SchemaVersion, knownVersions) {
		return Envelope{}, fmt.Errorf("%w: %d", ErrUnknownVersion, envelope.SchemaVersion)
	}
	for name, value := range map[string]string{
		"eventId":   envelope.EventID,
		"eventType": envelope.EventType,
		"source":    envelope.Source,
		"orgId":     envelope.OrgID,
	} {
		if value == "" {
			return Envelope{}, fmt.Errorf("%w: %s", ErrMissingField, name)
		}
	}
	if envelope.OccurredAt.IsZero() {
		return Envelope{}, fmt.Errorf("%w: occurredAt", ErrMissingField)
	}

	for name, identifier := range map[string]string{"eventId": envelope.EventID, "orgId": envelope.OrgID} {
		if _, err := uuid.Parse(identifier); err != nil {
			return Envelope{}, fmt.Errorf("%w: %s is not a UUID", ErrInvalidField, name)
		}
	}
	return envelope, nil
}

func isKnownVersion(version int, knownVersions []int) bool {
	for _, known := range knownVersions {
		if known == version {
			return true
		}
	}
	return false
}
