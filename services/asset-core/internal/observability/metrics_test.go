package observability_test

import (
	"strings"
	"testing"
	"time"

	"seta-im-intern/go-asset-core/internal/observability"
)

func TestRenderPrometheusMetricsUsesCumulativeBuckets(t *testing.T) {
	observability.ResetMetricsForTests()
	t.Cleanup(observability.ResetMetricsForTests)

	observability.RecordHTTP("GET", "/internal/api/v1/facts/folders", 200, 125*time.Millisecond)
	observability.RecordHTTP("GET", "/internal/api/v1/facts/folders", 200, 750*time.Millisecond)

	rendered := observability.RenderPrometheusMetrics()
	if !strings.Contains(rendered, `seta_asset_http_requests_total{method="GET",route="/internal/api/v1/facts/folders",status="200",result="success"} 2`) {
		t.Fatalf("missing request total: %s", rendered)
	}
	if !strings.Contains(rendered, `seta_asset_http_request_duration_seconds_bucket{method="GET",route="/internal/api/v1/facts/folders",status="200",result="success",le="0.25"} 1`) {
		t.Fatalf("missing 250ms bucket: %s", rendered)
	}
	if !strings.Contains(rendered, `seta_asset_http_request_duration_seconds_bucket{method="GET",route="/internal/api/v1/facts/folders",status="200",result="success",le="1"} 2`) {
		t.Fatalf("missing 1s bucket: %s", rendered)
	}
}
