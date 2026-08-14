import type { FastifyReply, FastifyRequest } from "fastify";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  recordHttpRequest,
  renderPrometheusMetrics,
  resetPrometheusMetricsForTests,
} from "../observability/prometheus";
import { logRequestCompletion } from "../observability/requestLogging";

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
    expect(rendered).toContain("seta_access_media_asset_breaker_in_flight");
    expect(rendered).toContain('seta_access_media_asset_breaker_state{state="closed"}');
  });

  it("does not collect request metrics when metrics are disabled", () => {
    const request = {
      correlation: { traceId: "trace", requestId: "request", startedAt: Date.now() },
      log: { info: vi.fn() },
      method: "GET",
      routeOptions: { url: "/health" },
      url: "/health",
    } as unknown as FastifyRequest;
    const reply = { statusCode: 200 } as FastifyReply;

    logRequestCompletion(request, reply, false);

    expect(renderPrometheusMetrics()).not.toContain("seta_access_http_requests_total{");
  });

  it("uses one stable label for all unmatched routes", () => {
    const reply = { statusCode: 404 } as FastifyReply;
    for (const url of ["/unknown/a", "/unknown/b"]) {
      const request = {
        correlation: {
          traceId: "trace",
          requestId: "request",
          startedAt: Date.now(),
        },
        log: { info: vi.fn() },
        method: "GET",
        routeOptions: {},
        url,
      } as unknown as FastifyRequest;
      logRequestCompletion(request, reply, true);
    }

    const rendered = renderPrometheusMetrics();
    expect(rendered).toContain(
      'seta_access_http_requests_total{method="GET",route="unmatched",status="404",result="denied"} 2',
    );
    expect(rendered).not.toContain("/unknown/");
  });

  it("folds arbitrary HTTP methods into one bounded label", () => {
    recordHttpRequest("ATTACK-A", "/graphql", 404, "denied", 1);
    recordHttpRequest("ATTACK-B", "/graphql", 404, "denied", 1);

    const rendered = renderPrometheusMetrics();
    expect(rendered).toContain(
      'seta_access_http_requests_total{method="OTHER",route="/graphql",status="404",result="denied"} 2',
    );
    expect(rendered).not.toContain("ATTACK-");
  });
});
