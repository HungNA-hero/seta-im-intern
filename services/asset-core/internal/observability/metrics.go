package observability

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"seta-im-intern/go-asset-core/internal/eventing"
)

var durationBucketsSeconds = []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 3, 5}
var metricsEnabled atomic.Bool

type httpKey struct {
	Method string
	Route  string
	Status int
	Result string
}

type httpStats struct {
	Count      uint64
	SumSeconds float64
	Buckets    []uint64
}

var httpMetrics = struct {
	sync.Mutex
	Requests map[httpKey]*httpStats
}{Requests: make(map[httpKey]*httpStats)}

func resultForStatus(status int) string {
	if status >= http.StatusInternalServerError {
		return "failure"
	}
	if status >= http.StatusBadRequest {
		return "denied"
	}
	return "success"
}

func boundedHTTPMethod(method string) string {
	switch strings.ToUpper(method) {
	case http.MethodConnect, http.MethodDelete, http.MethodGet, http.MethodHead,
		http.MethodOptions, http.MethodPatch, http.MethodPost, http.MethodPut, http.MethodTrace:
		return strings.ToUpper(method)
	default:
		return "OTHER"
	}
}

// RecordHTTP stores bounded-cardinality HTTP metrics. Callers pass route patterns,
// not user-controlled URLs, to avoid turning labels into unbounded storage.
func RecordHTTP(method, route string, status int, duration time.Duration) {
	if !metricsEnabled.Load() {
		return
	}
	key := httpKey{Method: boundedHTTPMethod(method), Route: route, Status: status, Result: resultForStatus(status)}
	durationSeconds := max(0, duration.Seconds())

	httpMetrics.Lock()
	defer httpMetrics.Unlock()
	stats, ok := httpMetrics.Requests[key]
	if !ok {
		stats = &httpStats{Buckets: make([]uint64, len(durationBucketsSeconds))}
		httpMetrics.Requests[key] = stats
	}
	stats.Count++
	stats.SumSeconds += durationSeconds
	for index, bucket := range durationBucketsSeconds {
		if durationSeconds <= bucket {
			stats.Buckets[index]++
		}
	}
}

// SetMetricsEnabled controls metric collection as well as endpoint exposure.
func SetMetricsEnabled(enabled bool) {
	metricsEnabled.Store(enabled)
}

func prometheusLabels(values map[string]string) string {
	labels := make([]string, 0, len(values))
	for _, key := range []string{"method", "route", "status", "result", "le", "outcome"} {
		value, ok := values[key]
		if !ok {
			continue
		}
		escaped := strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\"", "\\\"").Replace(value)
		labels = append(labels, fmt.Sprintf(`%s="%s"`, key, escaped))
	}
	return "{" + strings.Join(labels, ",") + "}"
}

// RenderPrometheusMetrics returns the Prometheus text exposition format without a client SDK.
func RenderPrometheusMetrics() string {
	httpMetrics.Lock()
	defer httpMetrics.Unlock()

	var output strings.Builder
	fmt.Fprintln(&output, "# HELP seta_asset_http_requests_total Completed Asset Core HTTP requests.")
	fmt.Fprintln(&output, "# TYPE seta_asset_http_requests_total counter")
	for key, stats := range httpMetrics.Requests {
		requestLabels := map[string]string{
			"method": key.Method,
			"route":  key.Route,
			"status": fmt.Sprint(key.Status),
			"result": key.Result,
		}
		fmt.Fprintf(&output, "seta_asset_http_requests_total%s %d\n", prometheusLabels(requestLabels), stats.Count)
	}

	fmt.Fprintln(&output, "# HELP seta_asset_http_request_duration_seconds Asset Core HTTP request duration.")
	fmt.Fprintln(&output, "# TYPE seta_asset_http_request_duration_seconds histogram")
	for key, stats := range httpMetrics.Requests {
		requestLabels := map[string]string{
			"method": key.Method,
			"route":  key.Route,
			"status": fmt.Sprint(key.Status),
			"result": key.Result,
		}
		for index, bucket := range durationBucketsSeconds {
			fmt.Fprintf(&output, "seta_asset_http_request_duration_seconds_bucket%s %d\n", prometheusLabels(mergeLabels(requestLabels, "le", fmt.Sprint(bucket))), stats.Buckets[index])
		}
		fmt.Fprintf(&output, "seta_asset_http_request_duration_seconds_bucket%s %d\n", prometheusLabels(mergeLabels(requestLabels, "le", "+Inf")), stats.Count)
		fmt.Fprintf(&output, "seta_asset_http_request_duration_seconds_sum%s %g\n", prometheusLabels(requestLabels), stats.SumSeconds)
		fmt.Fprintf(&output, "seta_asset_http_request_duration_seconds_count%s %d\n", prometheusLabels(requestLabels), stats.Count)
	}

	events := eventing.Metrics()
	fmt.Fprintln(&output, "# HELP seta_asset_lifecycle_events_total Asset lifecycle event publish outcomes.")
	fmt.Fprintln(&output, "# TYPE seta_asset_lifecycle_events_total counter")
	fmt.Fprintf(&output, "seta_asset_lifecycle_events_total%s %d\n", prometheusLabels(map[string]string{"outcome": "published"}), events.PublishedTotal)
	fmt.Fprintf(&output, "seta_asset_lifecycle_events_total%s %d\n", prometheusLabels(map[string]string{"outcome": "lost"}), events.LostPublishTotal)

	return output.String()
}

func mergeLabels(values map[string]string, key, value string) map[string]string {
	merged := make(map[string]string, len(values)+1)
	for existingKey, existingValue := range values {
		merged[existingKey] = existingValue
	}
	merged[key] = value
	return merged
}

// ResetMetricsForTests avoids cross-test state when this package is tested directly.
func ResetMetricsForTests() {
	metricsEnabled.Store(false)
	httpMetrics.Lock()
	defer httpMetrics.Unlock()
	httpMetrics.Requests = make(map[httpKey]*httpStats)
}

func MetricsHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(RenderPrometheusMetrics()))
}
