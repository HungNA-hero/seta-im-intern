package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
)

type replayPayload struct {
	EventID     string `json:"eventId"`
	AggregateID string `json:"aggregateId"`
}

const (
	testReplayQuarantineID = "64f4f7570b3f7b1ec67f1ea7a80ff2ec9f44acb91544a456b820087aa62ed273"
	testReplayJobID        = "a74e1124-b5c0-47b4-b73f-4ce7c7031d77"
)

type fakeReplayStore struct {
	mu          sync.Mutex
	aggregateID string
	isolated    bool
	err         error
	requests    []ReplayRequest
	eventIDs    []uuid.UUID
	payloads    [][]byte
}

// RebuildAndEnqueue models the adapter's required single transaction: it checks
// isolation, generates the ID, and constructs the payload from current state
// with that same ID before clearing isolation.
func (fake *fakeReplayStore) RebuildAndEnqueue(_ context.Context, request ReplayRequest) (uuid.UUID, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.err != nil {
		return uuid.Nil, fake.err
	}
	if !fake.isolated {
		return uuid.Nil, ErrNotIsolated
	}
	eventID := uuid.New()
	payload, err := json.Marshal(replayPayload{EventID: eventID.String(), AggregateID: fake.aggregateID})
	if err != nil {
		return uuid.Nil, err
	}
	fake.requests = append(fake.requests, request)
	fake.eventIDs = append(fake.eventIDs, eventID)
	fake.payloads = append(fake.payloads, payload)
	fake.isolated = false
	return eventID, nil
}

func TestReplayAtomicallyRebuildsCurrentStateWithTheFreshEventID(t *testing.T) {
	store := &fakeReplayStore{
		aggregateID: "a74e1124-b5c0-47b4-b73f-4ce7c7031d77",
		isolated:    true,
	}

	eventID, err := Replay(context.Background(), store, ReplayRequest{
		QuarantineID: testReplayQuarantineID,
		JobID:        testReplayJobID,
		Operator:     "ops-alice",
	})
	if err != nil {
		t.Fatalf("Replay returned error: %v", err)
	}
	if eventID == uuid.Nil || len(store.eventIDs) != 1 || store.eventIDs[0] != eventID {
		t.Fatalf("returned event ID = %s, stored IDs = %v; replay row and result must share one fresh ID", eventID, store.eventIDs)
	}
	if len(store.requests) != 1 || store.requests[0].QuarantineID != testReplayQuarantineID || store.requests[0].Operator != "ops-alice" {
		t.Fatalf("requests = %+v, want the quarantine and auditable operator passed into the transaction", store.requests)
	}

	var payload replayPayload
	if err := json.Unmarshal(store.payloads[0], &payload); err != nil {
		t.Fatalf("decoding rebuilt payload: %v", err)
	}
	if payload.EventID != eventID.String() {
		t.Fatalf("payload eventId = %q, want outbox event ID %q", payload.EventID, eventID)
	}
	if payload.AggregateID != store.aggregateID {
		t.Fatalf("payload aggregateId = %q, want current database value %q", payload.AggregateID, store.aggregateID)
	}
}

func TestReplayRefusesWithoutAnOperatorIdentity(t *testing.T) {
	store := &fakeReplayStore{isolated: true}

	_, err := Replay(context.Background(), store, ReplayRequest{QuarantineID: testReplayQuarantineID, JobID: testReplayJobID})

	if !errors.Is(err, ErrOperatorRequired) {
		t.Fatalf("err = %v, want ErrOperatorRequired — replay is never automatic", err)
	}
	if len(store.eventIDs) != 0 {
		t.Fatalf("an unattributed replay enqueued %d events, want 0", len(store.eventIDs))
	}
}

func TestReplayRefusesWithoutAJobCorrelation(t *testing.T) {
	store := &fakeReplayStore{isolated: true}

	_, err := Replay(context.Background(), store, ReplayRequest{
		QuarantineID: testReplayQuarantineID,
		Operator:     "ops-alice",
	})

	if !errors.Is(err, ErrJobRequired) {
		t.Fatalf("err = %v, want ErrJobRequired", err)
	}
	if len(store.eventIDs) != 0 {
		t.Fatalf("an uncorrelated replay enqueued %d events, want 0", len(store.eventIDs))
	}
}

func TestReplayRefusesAnInvalidQuarantineIdentity(t *testing.T) {
	store := &fakeReplayStore{isolated: true}

	_, err := Replay(context.Background(), store, ReplayRequest{
		QuarantineID: "deadbeef",
		JobID:        testReplayJobID,
		Operator:     "ops-alice",
	})

	if !errors.Is(err, ErrInvalidQuarantineID) {
		t.Fatalf("err = %v, want ErrInvalidQuarantineID", err)
	}
	if len(store.eventIDs) != 0 {
		t.Fatalf("an invalid replay enqueued %d events, want 0", len(store.eventIDs))
	}
}

func TestConcurrentReplayCannotClearIsolationTwice(t *testing.T) {
	store := &fakeReplayStore{isolated: true}
	errorsByOperator := make(chan error, 2)
	for _, operator := range []string{"ops-alice", "ops-bob"} {
		go func() {
			_, err := Replay(context.Background(), store, ReplayRequest{
				QuarantineID: testReplayQuarantineID,
				JobID:        testReplayJobID,
				Operator:     operator,
			})
			errorsByOperator <- err
		}()
	}

	var successes, notIsolated int
	for range 2 {
		err := <-errorsByOperator
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrNotIsolated):
			notIsolated++
		default:
			t.Fatalf("concurrent Replay returned unexpected error: %v", err)
		}
	}
	if successes != 1 || notIsolated != 1 {
		t.Fatalf("successes = %d, not-isolated = %d; want exactly one atomic replay winner", successes, notIsolated)
	}
	if len(store.eventIDs) != 1 {
		t.Fatalf("enqueued %d events, want exactly one", len(store.eventIDs))
	}
}

func TestReplayPropagatesAtomicStoreFailure(t *testing.T) {
	store := &fakeReplayStore{isolated: true, err: errors.New("transaction failed")}

	eventID, err := Replay(context.Background(), store, ReplayRequest{
		QuarantineID: testReplayQuarantineID,
		JobID:        testReplayJobID,
		Operator:     "ops-alice",
	})

	if err == nil {
		t.Fatal("Replay returned no error when the atomic store transaction failed")
	}
	if eventID != uuid.Nil {
		t.Fatalf("event ID = %s, want nil because no outbox row committed", eventID)
	}
}

type missingIDReplayStore struct{}

func (missingIDReplayStore) RebuildAndEnqueue(context.Context, ReplayRequest) (uuid.UUID, error) {
	return uuid.Nil, nil
}

func TestReplayRejectsACommittedTransactionWithoutAnEventID(t *testing.T) {
	eventID, err := Replay(context.Background(), missingIDReplayStore{}, ReplayRequest{
		QuarantineID: testReplayQuarantineID,
		JobID:        testReplayJobID,
		Operator:     "ops-alice",
	})

	if !errors.Is(err, ErrMissingReplayID) {
		t.Fatalf("err = %v, want ErrMissingReplayID", err)
	}
	if eventID != uuid.Nil {
		t.Fatalf("event ID = %s, want nil", eventID)
	}
}
