package requestcontext

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

type contextKey string

const (
	actorKey       = contextKey("actor")
	correlationKey = contextKey("correlation")
)

type Actor struct {
	UserID string
	OrgID  string
}

type Correlation struct {
	TraceID     string
	RequestID   string
	StartedAt   time.Time
	ErrorCode   string
	ErrorNumber int
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

func WithCorrelation(ctx context.Context, correlation *Correlation) context.Context {
	return context.WithValue(ctx, correlationKey, correlation)
}

func GetCorrelation(ctx context.Context) *Correlation {
	correlation, _ := ctx.Value(correlationKey).(*Correlation)
	return correlation
}

func TraceID(ctx context.Context) string {
	if correlation := GetCorrelation(ctx); correlation != nil && correlation.TraceID != "" {
		return correlation.TraceID
	}
	return randomHex(16)
}

func RecordError(ctx context.Context, code string, number int) {
	if correlation := GetCorrelation(ctx); correlation != nil {
		correlation.ErrorCode = code
		correlation.ErrorNumber = number
	}
}

func ParseTraceparent(value string) (string, bool) {
	parts := strings.Split(strings.TrimSpace(value), "-")
	if len(parts) != 4 || len(parts[0]) != 2 || len(parts[1]) != 32 || len(parts[2]) != 16 || len(parts[3]) != 2 {
		return "", false
	}
	if strings.EqualFold(parts[0], "ff") || allZero(parts[1]) || allZero(parts[2]) {
		return "", false
	}
	for _, part := range parts {
		if _, err := hex.DecodeString(part); err != nil {
			return "", false
		}
	}
	return strings.ToLower(parts[1]), true
}

func NewTraceparent(traceID string) string {
	return "00-" + traceID + "-" + randomHex(8) + "-01"
}

func randomHex(bytes int) string {
	buffer := make([]byte, bytes)
	if _, err := rand.Read(buffer); err != nil {
		panic("unable to generate request correlation identifier")
	}
	return hex.EncodeToString(buffer)
}

func allZero(value string) bool {
	return strings.Trim(value, "0") == ""
}
