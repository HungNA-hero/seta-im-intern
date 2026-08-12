package outbox

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

type fakeReplayStore struct {
	subject    ReplaySubject
	loadErr    error
	enqueued   []ReplaySubject
	eventIDs   []uuid.UUID
	operators  []string
	loadedFrom []string
}

func (fake *fakeReplayStore) LoadIsolated(_ context.Context, quarantineID string) (ReplaySubject, error) {
	fake.loadedFrom = append(fake.loadedFrom, quarantineID)
	if fake.loadErr != nil {
		return ReplaySubject{}, fake.loadErr
	}
	return fake.subject, nil
}

func (fake *fakeReplayStore) ClearIsolationAndEnqueue(_ context.Context, subject ReplaySubject, eventID uuid.UUID, operator string) error {
	fake.enqueued = append(fake.enqueued, subject)
	fake.eventIDs = append(fake.eventIDs, eventID)
	fake.operators = append(fake.operators, operator)
	return nil
}

func databaseTruth() ReplaySubject {
	return ReplaySubject{
		AggregateID: "a74e1124-b5c0-47b4-b73f-4ce7c7031d77",
		Topic:       "media-processing.v1",
		Key:         "a74e1124-b5c0-47b4-b73f-4ce7c7031d77",
		Payload:     []byte(`{"eventId":"rebuilt-from-database","schemaVersion":1}`),
		Isolated:    true,
	}
}

func TestReplayRebuildsTheEventFromDatabaseTruthUnderAFreshEventID(t *testing.T) {
	store := &fakeReplayStore{subject: databaseTruth()}

	firstEventID, err := Replay(context.Background(), store, ReplayRequest{
		QuarantineID: "deadbeef",
		Operator:     "ops-alice",
	})
	if err != nil {
		t.Fatalf("Replay returned error: %v", err)
	}

	if len(store.loadedFrom) != 1 || store.loadedFrom[0] != "deadbeef" {
		t.Fatalf("loaded from %v, want the quarantine identity [deadbeef]", store.loadedFrom)
	}
	if len(store.enqueued) != 1 {
		t.Fatalf("enqueued %d events, want 1", len(store.enqueued))
	}
	if string(store.enqueued[0].Payload) != string(databaseTruth().Payload) {
		t.Fatalf("enqueued payload = %s, want the payload rebuilt from database truth", store.enqueued[0].Payload)
	}
	if store.operators[0] != "ops-alice" {
		t.Fatalf("operator = %q, want the auditable operator identity", store.operators[0])
	}
	if firstEventID == uuid.Nil {
		t.Fatal("replay produced no event ID")
	}

	secondEventID, err := Replay(context.Background(), store, ReplayRequest{QuarantineID: "deadbeef", Operator: "ops-alice"})
	if err != nil {
		t.Fatalf("second Replay returned error: %v", err)
	}
	if secondEventID == firstEventID {
		t.Fatal("a second replay reused the first event ID, so the two replays are indistinguishable")
	}
}

func TestReplayRefusesWithoutAnOperatorIdentity(t *testing.T) {
	store := &fakeReplayStore{subject: databaseTruth()}

	_, err := Replay(context.Background(), store, ReplayRequest{QuarantineID: "deadbeef"})

	if !errors.Is(err, ErrOperatorRequired) {
		t.Fatalf("err = %v, want ErrOperatorRequired — replay is never automatic", err)
	}
	if len(store.enqueued) != 0 {
		t.Fatalf("an unattributed replay enqueued %d events, want 0", len(store.enqueued))
	}
}

func TestReplayRefusesASubjectThatIsNotIsolated(t *testing.T) {
	subject := databaseTruth()
	subject.Isolated = false
	store := &fakeReplayStore{subject: subject}

	_, err := Replay(context.Background(), store, ReplayRequest{QuarantineID: "deadbeef", Operator: "ops-alice"})

	if !errors.Is(err, ErrNotIsolated) {
		t.Fatalf("err = %v, want ErrNotIsolated", err)
	}
	if len(store.enqueued) != 0 {
		t.Fatalf("replay enqueued %d events for a healthy subject, want 0", len(store.enqueued))
	}
}
