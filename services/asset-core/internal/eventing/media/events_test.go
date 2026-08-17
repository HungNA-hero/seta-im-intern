package media_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"seta-im-intern/go-asset-core/internal/eventing/event"
	"seta-im-intern/go-asset-core/internal/eventing/media"
)

func validPayload() media.Payload {
	return media.Payload{
		AssetID:   uuid.NewString(),
		UploadID:  uuid.NewString(),
		VersionID: uuid.NewString(),
		JobID:     uuid.NewString(),
	}
}

func validEnvelope() event.Envelope {
	return event.Envelope{
		EventID:    uuid.NewString(),
		OrgID:      uuid.NewString(),
		OccurredAt: time.Date(2026, time.August, 14, 9, 30, 0, 0, time.UTC),
	}
}

func TestMarshalProducesARecordTheSharedParserAccepts(t *testing.T) {
	envelope, payload := validEnvelope(), validPayload()

	value, err := media.Marshal(envelope, payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	parsed, err := event.Parse(value, []int{media.SchemaVersion})
	if err != nil {
		t.Fatalf("the shared parser rejected a record we produced: %v", err)
	}
	if parsed.EventType != media.EventType {
		t.Errorf("eventType = %q, want %q", parsed.EventType, media.EventType)
	}
	if parsed.Source != media.Source {
		t.Errorf("source = %q, want %q", parsed.Source, media.Source)
	}
	if parsed.SchemaVersion != media.SchemaVersion {
		t.Errorf("schemaVersion = %d, want %d", parsed.SchemaVersion, media.SchemaVersion)
	}
	if parsed.EventID != envelope.EventID || parsed.OrgID != envelope.OrgID {
		t.Errorf("identity was not carried through: %+v", parsed)
	}

	recovered, err := media.Parse(parsed)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if recovered != payload {
		t.Errorf("payload round trip = %+v, want %+v", recovered, payload)
	}
}

// The envelope owns the generic fields, so a caller cannot mislabel an event by
// setting them; Marshal stamps its own.
func TestMarshalOverridesCallerSuppliedEnvelopeTypeAndSource(t *testing.T) {
	envelope := validEnvelope()
	envelope.EventType = "folder.deleted"
	envelope.Source = "somewhere-else"
	envelope.SchemaVersion = 99

	value, err := media.Marshal(envelope, validPayload())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var wire map[string]any
	if err := json.Unmarshal(value, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if wire["eventType"] != media.EventType {
		t.Errorf("eventType = %v, want %q", wire["eventType"], media.EventType)
	}
	if wire["source"] != media.Source {
		t.Errorf("source = %v, want %q", wire["source"], media.Source)
	}
	if wire["schemaVersion"] != float64(media.SchemaVersion) {
		t.Errorf("schemaVersion = %v, want %d", wire["schemaVersion"], media.SchemaVersion)
	}
}

func TestMarshalOmitsAnAbsentTraceparent(t *testing.T) {
	value, err := media.Marshal(validEnvelope(), validPayload())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var wire map[string]any
	if err := json.Unmarshal(value, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := wire["traceparent"]; present {
		t.Error("an absent traceparent must not be serialized as an empty field")
	}
}

func TestMarshalRejectsIdentifiersTheConsumerWouldQuarantine(t *testing.T) {
	cases := map[string]struct {
		mutate func(*media.Payload)
		want   error
	}{
		"missing jobId":   {func(payload *media.Payload) { payload.JobID = "" }, media.ErrMissingIdentifier},
		"missing assetId": {func(payload *media.Payload) { payload.AssetID = "" }, media.ErrMissingIdentifier},
		"jobId not a UUID": {
			func(payload *media.Payload) { payload.JobID = "job-1" },
			media.ErrInvalidIdentifier,
		},
		"versionId not a UUID": {
			func(payload *media.Payload) { payload.VersionID = "../../etc/passwd" },
			media.ErrInvalidIdentifier,
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			payload := validPayload()
			testCase.mutate(&payload)

			if _, err := media.Marshal(validEnvelope(), payload); !errors.Is(err, testCase.want) {
				t.Errorf("error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestMarshalRejectsAZeroOccurredAt(t *testing.T) {
	envelope := validEnvelope()
	envelope.OccurredAt = time.Time{}

	if _, err := media.Marshal(envelope, validPayload()); !errors.Is(err, media.ErrMissingIdentifier) {
		t.Errorf("error = %v, want %v", err, media.ErrMissingIdentifier)
	}
}

// The size ceiling is contracted, and the only unbounded field an envelope
// carries is the traceparent, so that is what the guard has to catch.
func TestMarshalRejectsARecordOverTheContractedCeiling(t *testing.T) {
	envelope := validEnvelope()
	envelope.Traceparent = strings.Repeat("a", media.MaxRecordBytes)

	if _, err := media.Marshal(envelope, validPayload()); !errors.Is(err, media.ErrRecordTooLarge) {
		t.Errorf("error = %v, want %v", err, media.ErrRecordTooLarge)
	}
}

func TestParseRejectsAPayloadMissingItsIdentifiers(t *testing.T) {
	envelope := validEnvelope()
	envelope.EventType = media.EventType
	envelope.SchemaVersion = media.SchemaVersion
	envelope.Source = media.Source
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	parsed, err := event.Parse(raw, []int{media.SchemaVersion})
	if err != nil {
		t.Fatalf("the envelope itself should be valid: %v", err)
	}
	if _, err := media.Parse(parsed); !errors.Is(err, media.ErrMissingIdentifier) {
		t.Errorf("error = %v, want %v", err, media.ErrMissingIdentifier)
	}
}
