import { describe, expect, it } from "vitest";
import { GraphQLError } from "graphql";
import { maskGraphQLError } from "../server";
import {
  createRequestCorrelation,
  getRequestCorrelation,
  parseTraceparent,
  runWithRequestCorrelation,
} from "../observability/requestContext";

describe("request correlation", () => {
  it("accepts a valid W3C traceparent and preserves its trace id", () => {
    const traceId = "a".repeat(32);
    const correlation = createRequestCorrelation({
      traceparent: `00-${traceId}-${"b".repeat(16)}-01`,
      "x-request-id": "KAN-57:request-1",
    });

    expect(correlation.traceId).toBe(traceId);
    expect(correlation.requestId).toBe("KAN-57:request-1");
    expect(correlation.traceparent).toMatch(
      new RegExp(`^00-${traceId}-[a-f0-9]{16}-01$`),
    );
  });

  it("regenerates correlation for invalid trace input", () => {
    const correlation = createRequestCorrelation({
      traceparent: `00-${"0".repeat(32)}-${"b".repeat(16)}-01`,
      "x-request-id": "contains spaces",
    });

    expect(parseTraceparent(`00-${"0".repeat(32)}-${"b".repeat(16)}-01`)).toBeNull();
    expect(correlation.traceId).toMatch(/^[a-f0-9]{32}$/);
    expect(correlation.traceId).not.toBe("0".repeat(32));
    expect(correlation.requestId).toMatch(
      /^[a-f0-9-]{36}$/,
    );
  });

  it("masks known errors with the stable registry and active trace", () => {
    const correlation = createRequestCorrelation({});
    const masked = runWithRequestCorrelation(correlation, () =>
      maskGraphQLError(
        new GraphQLError("resolver diagnostic", {
          extensions: { code: "BAD_USER_INPUT" },
        }),
        "unused fallback",
      ),
    ) as GraphQLError;

    expect(masked.message).toBe("Malformed request body or parameters");
    expect(masked.extensions).toMatchObject({
      code: "BAD_REQUEST",
      number: 1001,
      traceId: correlation.traceId,
      service: "access-core",
    });
    expect(getRequestCorrelation()).toBeUndefined();
  });

  it("preserves a validated Asset Core error origin and trace", () => {
    const assetTraceId = "d".repeat(32);
    const masked = maskGraphQLError(
      new GraphQLError("ignored", {
        extensions: {
          code: "METADATA_NOT_FOUND",
          number: 4001,
          traceId: assetTraceId,
          service: "asset-core",
        },
      }),
      "unused fallback",
    ) as GraphQLError;

    expect(masked.extensions).toMatchObject({
      code: "METADATA_NOT_FOUND",
      number: 4001,
      traceId: assetTraceId,
      service: "asset-core",
    });
  });
});
