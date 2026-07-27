import { afterEach, describe, expect, it } from "vitest";
import {
  recordHttpRequest,
  renderPrometheusMetrics,
  resetPrometheusMetricsForTests,
} from "../observability/prometheus";

afterEach(() => {
  resetPrometheusMetricsForTests();
});

describe("Prometheus metrics", () => {
  it("renders cumulative request histogram buckets and stable labels", () => {
    recordHttpRequest("POST", "/graphql", 200, "success", 125);
    recordHttpRequest("POST", "/graphql", 200, "success", 750);

    const rendered = renderPrometheusMetrics();

    expect(rendered).toContain(
      'seta_access_http_requests_total{method="POST",route="/graphql",status="200",result="success"} 2',
    );
    expect(rendered).toContain(
      'seta_access_http_request_duration_seconds_bucket{method="POST",route="/graphql",status="200",result="success",le="0.25"} 1',
    );
    expect(rendered).toContain(
      'seta_access_http_request_duration_seconds_bucket{method="POST",route="/graphql",status="200",result="success",le="1"} 2',
    );
    expect(rendered).toContain("seta_access_asset_breaker_in_flight");
  });
});
