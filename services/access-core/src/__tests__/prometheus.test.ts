import type { FastifyReply, FastifyRequest } from "fastify";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  recordHttpRequest,
  recordMediaRejection,
  recordMediaSessionCreation,
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

  it("renders the bounded direct-upload and media-route contract without tenant labels", () => {
    recordMediaSessionCreation("created", 2048);
    recordMediaSessionCreation("replayed", 4096);
    for (const reason of ["rate_limited", "checksum", "descriptor", "authorization", "dependency"] as const) {
      recordMediaRejection(reason);
    }
    recordMediaRejection("33333333-3333-4333-8333-333333333333" as never);
    recordHttpRequest("POST", "/api/v1/assets/:assetId/media/uploads", 201, "success", 125);

    const rendered = renderPrometheusMetrics();
    expect(rendered).toContain('seta_access_media_sessions_total{outcome="created"} 1');
    expect(rendered).toContain('seta_access_media_declared_bytes_total{outcome="created"} 2048');
    expect(rendered).toContain('seta_access_media_rejections_total{reason="rate_limited"} 1');
    expect(rendered).toContain('seta_access_media_rejections_total{reason="other"} 1');
    expect(rendered).toContain(
      'seta_access_media_route_duration_seconds_bucket{method="POST",route="/api/v1/assets/:assetId/media/uploads",status="201",result="success",le="0.25"} 1',
    );
    expect(rendered).not.toContain("33333333-3333-4333-8333-333333333333");
  });

  it("renders one unique media histogram series for each bounded HTTP status", () => {
    recordHttpRequest("POST", "/api/v1/assets/:assetId/media/uploads", 200, "success", 10);
    recordHttpRequest("POST", "/api/v1/assets/:assetId/media/uploads", 201, "success", 20);

    const sampleNames = renderPrometheusMetrics()
      .split("\n")
      .filter((line) => line.startsWith("seta_access_media_route_duration_seconds_"))
      .map((line) => line.slice(0, line.lastIndexOf(" ")));

    expect(new Set(sampleNames).size).toBe(sampleNames.length);
    expect(sampleNames.some((line) => line.includes('status="200"'))).toBe(true);
    expect(sampleNames.some((line) => line.includes('status="201"'))).toBe(true);
  });
});
