package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"seta-im-intern/go-asset-core/internal/observability"
)

func TestWorkerMetricsServerExposesOnlyMetrics(t *testing.T) {
	observability.ResetMetricsForTests()
	observability.SetMetricsEnabled(true)
	t.Cleanup(observability.ResetMetricsForTests)

	server := newWorkerMetricsServer()
	recorder := httptest.NewRecorder()
	server.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, want 200", recorder.Code)
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/plain") {
		t.Fatalf("metrics Content-Type = %q", contentType)
	}
	if !strings.Contains(recorder.Body.String(), "seta_asset_lifecycle_event_publish_total") {
		t.Fatalf("worker metrics missing producer outcome counter:\n%s", recorder.Body.String())
	}

	notFound := httptest.NewRecorder()
	server.Handler.ServeHTTP(notFound, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if notFound.Code != http.StatusNotFound {
		t.Fatalf("observability-only listener exposed an unexpected route: status=%d", notFound.Code)
	}
}
