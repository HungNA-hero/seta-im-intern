package event

import (
	"context"

	"seta-im-intern/go-asset-core/internal/requestcontext"
)

// Traceparent renders the current request's trace as a W3C traceparent, or an
// empty string when the event has no originating request. It never invents a
// trace: reconciliation sweeps and operator replays are legitimately
// uncorrelated, and a fabricated trace ID would imply a caller that never existed.
func Traceparent(ctx context.Context) string {
	correlation := requestcontext.GetCorrelation(ctx)
	if correlation == nil || correlation.TraceID == "" {
		return ""
	}
	traceparent := requestcontext.NewTraceparent(correlation.TraceID)
	if ParseTraceID(traceparent) == "" {
		return ""
	}
	return traceparent
}

// ParseTraceID returns the trace ID of a well-formed traceparent, or an empty
// string. Validation is shared with the HTTP path so one inbound traceparent
// cannot be accepted on the event path and rejected on the request path.
// Trace context is correlation only and is never used for authorization.
func ParseTraceID(traceparent string) string {
	traceID, ok := requestcontext.ParseTraceparent(traceparent)
	if !ok {
		return ""
	}
	return traceID
}
