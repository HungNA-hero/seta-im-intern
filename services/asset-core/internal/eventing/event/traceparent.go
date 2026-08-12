package event

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"strings"

	"seta-im-intern/go-asset-core/internal/requestcontext"
)

var (
	traceIDPattern = regexp.MustCompile(`^[\da-f]{32}$`)
	spanIDPattern  = regexp.MustCompile(`^[\da-f]{16}$`)
	nonZeroPattern = regexp.MustCompile(`[1-9a-f]`)
)

// Traceparent renders the current request's trace as a W3C traceparent, or an
// empty string when the event has no originating request. It never invents a
// trace: reconciliation sweeps and operator replays are legitimately
// uncorrelated, and a fabricated trace ID would imply a caller that never existed.
func Traceparent(ctx context.Context) string {
	correlation := requestcontext.GetCorrelation(ctx)
	if correlation == nil || !traceIDPattern.MatchString(correlation.TraceID) {
		return ""
	}
	return "00-" + correlation.TraceID + "-" + randomSpanID() + "-01"
}

// ParseTraceID returns the trace ID of a well-formed traceparent, or an empty
// string. Trace context is correlation only and is never used for authorization.
func ParseTraceID(traceparent string) string {
	parts := strings.Split(strings.TrimSpace(traceparent), "-")
	if len(parts) < 4 {
		return ""
	}

	version, traceID, spanID := parts[0], strings.ToLower(parts[1]), parts[2]
	if version == "ff" || !traceIDPattern.MatchString(traceID) || !spanIDPattern.MatchString(spanID) {
		return ""
	}
	if version == "00" && len(parts) != 4 {
		return ""
	}
	if !nonZeroPattern.MatchString(traceID) || !nonZeroPattern.MatchString(spanID) {
		return ""
	}
	return traceID
}

func randomSpanID() string {
	span := make([]byte, 8)
	if _, err := rand.Read(span); err != nil {
		return "0000000000000001"
	}
	return hex.EncodeToString(span)
}
