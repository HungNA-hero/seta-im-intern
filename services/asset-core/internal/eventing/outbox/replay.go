package outbox

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

type ReplaySubject struct {
	AggregateID string
	Topic       string
	Key         string
	Payload     []byte
	Isolated    bool
}

type ReplayRequest struct {
	QuarantineID string
	Operator     string
}

// ReplayStore rebuilds a replay subject from current database state. There is
// deliberately no parameter carrying a dead-letter payload: a replayed event is
// reconstructed from database truth, never from the stored quarantine record.
type ReplayStore interface {
	LoadIsolated(ctx context.Context, quarantineID string) (ReplaySubject, error)
	ClearIsolationAndEnqueue(ctx context.Context, subject ReplaySubject, eventID uuid.UUID, operator string) error
}

var (
	ErrOperatorRequired = errors.New("replay requires an operator identity")
	ErrNotIsolated      = errors.New("replay subject is not isolated")
)

// Replay is the only path back onto the main topic for a quarantined event, and
// it is operator-triggered. No consumer calls it.
func Replay(ctx context.Context, store ReplayStore, request ReplayRequest) (uuid.UUID, error) {
	if request.Operator == "" {
		return uuid.Nil, ErrOperatorRequired
	}

	subject, err := store.LoadIsolated(ctx, request.QuarantineID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("loading replay subject %s: %w", request.QuarantineID, err)
	}
	if !subject.Isolated {
		return uuid.Nil, fmt.Errorf("%w: %s", ErrNotIsolated, request.QuarantineID)
	}

	eventID := uuid.New()
	if err := store.ClearIsolationAndEnqueue(ctx, subject, eventID, request.Operator); err != nil {
		return uuid.Nil, fmt.Errorf("enqueueing replay of %s: %w", request.QuarantineID, err)
	}
	return eventID, nil
}
