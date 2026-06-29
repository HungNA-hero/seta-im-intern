package requestcontext

import (
	"context"
	"errors"
)

type contextKey string

const actorKey = contextKey("actor")

type Actor struct {
	UserID string
	OrgID  string
}

// WithActor injects the Actor into the context.
func WithActor(ctx context.Context, actor Actor) context.Context {
	return context.WithValue(ctx, actorKey, actor)
}

// GetActor extracts the Actor from the context.
func GetActor(ctx context.Context) (Actor, error) {
	actor, ok := ctx.Value(actorKey).(Actor)
	if !ok {
		return Actor{}, errors.New("actor not found in context")
	}
	return actor, nil
}
