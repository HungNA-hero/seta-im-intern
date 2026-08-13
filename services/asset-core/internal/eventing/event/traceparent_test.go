package event

import (
	"context"
	"regexp"
	"testing"

	"seta-im-intern/go-asset-core/internal/requestcontext"
)

var traceparentPattern = regexp.MustCompile(`^00-[\da-f]{32}-[\da-f]{16}-01$`)

func TestTraceparentIsEmptyWithoutAnOriginatingRequest(t *testing.T) {
	if traceparent := Traceparent(context.Background()); traceparent != "" {
		t.Fatalf("traceparent = %q, want empty when no request context exists", traceparent)
	}
}

func TestTraceparentCarriesTheRequestTraceID(t *testing.T) {
	traceID := "4bf92f3577b34da6a3ce929d0e0e4736"
	ctx := requestcontext.WithCorrelation(context.Background(), &requestcontext.Correlation{TraceID: traceID})

	traceparent := Traceparent(ctx)

	if !traceparentPattern.MatchString(traceparent) {
		t.Fatalf("traceparent = %q, want W3C format 00-<32 hex>-<16 hex>-01", traceparent)
	}
	if ParseTraceID(traceparent) != traceID {
		t.Fatalf("round-tripped trace ID = %q, want %q", ParseTraceID(traceparent), traceID)
	}
}

func TestParseTraceIDRejectsMalformedTraceparents(t *testing.T) {
	for _, malformed := range []string{
		"",
		"garbage",
		"00-tooshort-00f067aa0ba902b7-01",
		"00-00000000000000000000000000000000-00f067aa0ba902b7-01",
		"ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	} {
		if traceID := ParseTraceID(malformed); traceID != "" {
			t.Fatalf("ParseTraceID(%q) = %q, want empty", malformed, traceID)
		}
	}
}
