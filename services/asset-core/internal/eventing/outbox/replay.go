package outbox

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

type ReplayRequest struct {
	QuarantineID string
	Operator     string
}

// ReplayStore owns the complete replay transaction. RebuildAndEnqueue must lock
// the quarantine subject, verify it is still isolated, load current database
// truth, generate the event ID, build a current-schema payload containing that
// same ID, clear isolation, and insert the outbox row atomically. It must return
// ErrNotIsolated when the locked subject is no longer isolated.
type ReplayStore interface {
	RebuildAndEnqueue(ctx context.Context, request ReplayRequest) (uuid.UUID, error)
}

var (
	ErrOperatorRequired = errors.New("replay requires an operator identity")
	ErrNotIsolated      = errors.New("replay subject is not isolated")
	ErrMissingReplayID  = errors.New("replay transaction returned no event ID")
)

// Replay is the only path back onto the main topic for a quarantined event, and
// it is operator-triggered. No consumer calls it.
func Replay(ctx context.Context, store ReplayStore, request ReplayRequest) (uuid.UUID, error) {
	if request.Operator == "" {
		return uuid.Nil, ErrOperatorRequired
	}

	eventID, err := store.RebuildAndEnqueue(ctx, request)
	if err != nil {
		return uuid.Nil, fmt.Errorf("enqueueing replay of %s: %w", request.QuarantineID, err)
	}
	if eventID == uuid.Nil {
		return uuid.Nil, ErrMissingReplayID
	}
	return eventID, nil
}
