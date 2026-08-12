package event

import (
	"errors"
	"testing"
)

const wellFormed = `{"eventId":"6e2f14ea-d7c7-4f1c-8f17-274c51d9bcb9","eventType":"media.processing.requested",` +
	`"schemaVersion":1,"source":"asset-core","occurredAt":"2026-08-06T10:00:00Z",` +
	`"orgId":"10000000-0000-0000-0000-000000000001"}`

func TestParseAcceptsAWellFormedEnvelope(t *testing.T) {
	envelope, err := Parse([]byte(wellFormed), []int{1})
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if envelope.EventID != "6e2f14ea-d7c7-4f1c-8f17-274c51d9bcb9" {
		t.Fatalf("EventID = %q", envelope.EventID)
	}
}

func TestParseRejectsIdentifiersThatAreNotUUIDs(t *testing.T) {
	for name, value := range map[string]string{
		"eventId": `{"eventId":"not-a-uuid","eventType":"media.processing.requested","schemaVersion":1,` +
			`"source":"asset-core","occurredAt":"2026-08-06T10:00:00Z","orgId":"10000000-0000-0000-0000-000000000001"}`,
		"orgId": `{"eventId":"6e2f14ea-d7c7-4f1c-8f17-274c51d9bcb9","eventType":"media.processing.requested",` +
			`"schemaVersion":1,"source":"asset-core","occurredAt":"2026-08-06T10:00:00Z","orgId":"tenant-one"}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(value), []int{1})
			if !errors.Is(err, ErrInvalidField) {
				t.Fatalf("err = %v, want ErrInvalidField so a malformed identifier is immediate poison", err)
			}
		})
	}
}
