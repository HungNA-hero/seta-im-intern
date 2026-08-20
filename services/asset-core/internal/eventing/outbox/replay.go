package outbox

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

type ReplayRequest struct {
	QuarantineID string
	JobID        string
	Operator     string
}

type ReplayStore interface {
	RebuildAndEnqueue(ctx context.Context, request ReplayRequest) (uuid.UUID, error)
}

var (
	ErrOperatorRequired    = errors.New("replay requires an operator identity")
	ErrJobRequired         = errors.New("replay requires a correlated job identity")
	ErrInvalidQuarantineID = errors.New("replay requires a 64-character lowercase hexadecimal quarantine identity")
	ErrNotIsolated         = errors.New("replay subject is not isolated")
	ErrMissingReplayID     = errors.New("replay transaction returned no event ID")
)

func Replay(ctx context.Context, store ReplayStore, request ReplayRequest) (uuid.UUID, error) {
	if request.Operator == "" {
		return uuid.Nil, ErrOperatorRequired
	}
	if request.JobID == "" {
		return uuid.Nil, ErrJobRequired
	}
	if len(request.QuarantineID) != 64 || request.QuarantineID != strings.ToLower(request.QuarantineID) {
		return uuid.Nil, ErrInvalidQuarantineID
	}
	if _, err := hex.DecodeString(request.QuarantineID); err != nil {
		return uuid.Nil, ErrInvalidQuarantineID
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
