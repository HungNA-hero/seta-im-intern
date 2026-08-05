import { getAssetBreakerSnapshot } from "../clients/assetBreaker";
import { getMetricsSnapshotForTests } from "../cache/metrics";

const HTTP_DURATION_BUCKETS_SECONDS = [0.05, 0.1, 0.25, 0.5, 1, 2, 3, 5];

interface HttpRequestStats {
  count: number;
  sumSeconds: number;
  buckets: number[];
}

const httpRequests = new Map<string, HttpRequestStats>();
const KNOWN_HTTP_METHODS = new Set(["CONNECT", "DELETE", "GET", "HEAD", "OPTIONS", "PATCH", "POST", "PUT", "TRACE"]);

function escapeLabel(value: string): string {
  return value.replace(/\\/g, "\\\\").replace(/\n/g, "\\n").replace(/"/g, '\\"');
}

function labels(values: Record<string, string>): string {
  return `{${Object.entries(values)
    .map(([key, value]) => `${key}="${escapeLabel(value)}"`)
    .join(",")}}`;
}

function requestKey(method: string, route: string, status: number, result: string): string {
  return [method, route, String(status), result].join("\u0000");
}

export function recordHttpRequest(
  method: string,
  route: string,
  status: number,
  result: string,
  durationMs: number,
): void {
  const normalizedMethod = KNOWN_HTTP_METHODS.has(method.toUpperCase()) ? method.toUpperCase() : "OTHER";
  const key = requestKey(normalizedMethod, route, status, result);
  const current = httpRequests.get(key) ?? {
    count: 0,
    sumSeconds: 0,
    buckets: HTTP_DURATION_BUCKETS_SECONDS.map(() => 0),
  };
  const durationSeconds = Math.max(0, durationMs) / 1000;

  current.count += 1;
  current.sumSeconds += durationSeconds;
  HTTP_DURATION_BUCKETS_SECONDS.forEach((bucket, index) => {
    if (durationSeconds <= bucket) current.buckets[index] += 1;
  });
  httpRequests.set(key, current);
}

/** Renders the Prometheus text exposition format without adding a client SDK. */
export function renderPrometheusMetrics(): string {
  const lines = [
    "# HELP seta_access_http_requests_total Completed Access Core HTTP requests.",
    "# TYPE seta_access_http_requests_total counter",
  ];

  for (const [key, stats] of httpRequests) {
    const [method, route, status, result] = key.split("\u0000");
    const requestLabels = { method, route, status, result };
    lines.push(`seta_access_http_requests_total${labels(requestLabels)} ${stats.count}`);
  }

  lines.push(
    "# HELP seta_access_http_request_duration_seconds Access Core HTTP request duration.",
    "# TYPE seta_access_http_request_duration_seconds histogram",
  );
  for (const [key, stats] of httpRequests) {
    const [method, route, status, result] = key.split("\u0000");
    const requestLabels = { method, route, status, result };
    HTTP_DURATION_BUCKETS_SECONDS.forEach((bucket, index) => {
      lines.push(
        `seta_access_http_request_duration_seconds_bucket${labels({ ...requestLabels, le: String(bucket) })} ${stats.buckets[index]}`,
      );
    });
    lines.push(
      `seta_access_http_request_duration_seconds_bucket${labels({ ...requestLabels, le: "+Inf" })} ${stats.count}`,
      `seta_access_http_request_duration_seconds_sum${labels(requestLabels)} ${stats.sumSeconds}`,
      `seta_access_http_request_duration_seconds_count${labels(requestLabels)} ${stats.count}`,
    );
  }

  lines.push(
    "# HELP seta_access_events_total Access cache, invalidation, and breaker events.",
    "# TYPE seta_access_events_total counter",
  );
  const { counters, invalidationLatenciesMs } = getMetricsSnapshotForTests();
  for (const [event, count] of Object.entries(counters)) {
    lines.push(`seta_access_events_total${labels({ event })} ${count}`);
  }

  const invalidationSeconds = invalidationLatenciesMs.reduce(
    (total, latencyMs) => total + Math.max(0, latencyMs) / 1000,
    0,
  );
  lines.push(
    "# HELP seta_access_cache_invalidation_latency_seconds Completed cache invalidation latency samples.",
    "# TYPE seta_access_cache_invalidation_latency_seconds summary",
    `seta_access_cache_invalidation_latency_seconds_count ${invalidationLatenciesMs.length}`,
    `seta_access_cache_invalidation_latency_seconds_sum ${invalidationSeconds}`,
  );

  const breaker = getAssetBreakerSnapshot();
  lines.push(
    "# HELP seta_access_asset_breaker_in_flight Current Access to Asset requests admitted by the breaker.",
    "# TYPE seta_access_asset_breaker_in_flight gauge",
    `seta_access_asset_breaker_in_flight ${breaker.inFlight}`,
    "# HELP seta_access_asset_breaker_state Current Asset Circuit Breaker state; exactly one state is 1.",
    "# TYPE seta_access_asset_breaker_state gauge",
  );
  for (const state of ["closed", "open", "halfOpen"]) {
    lines.push(`seta_access_asset_breaker_state${labels({ state })} ${breaker.state === state ? 1 : 0}`);
  }

  return `${lines.join("\n")}\n`;
}

export function resetPrometheusMetricsForTests(): void {
  httpRequests.clear();
}
