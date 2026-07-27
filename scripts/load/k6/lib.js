import http from "k6/http";
import { check } from "k6";
import { Rate } from "k6/metrics";

export const graphqlFailures = new Rate("graphql_failures");

function fixedWidthHex(value, width) {
  return Math.max(1, value).toString(16).padStart(width, "0").slice(-width);
}

export function correlationHeaders() {
  const traceId = `${fixedWidthHex(__VU, 16)}${fixedWidthHex(__ITER + 1, 16)}`;
  return {
    "Content-Type": "application/json",
    traceparent: `00-${traceId}-${fixedWidthHex(__ITER + 1, 16)}-01`,
    "x-request-id": `k6-${__VU}-${__ITER}`,
    "x-user-id": __ENV.USER_ID,
    "x-org-id": __ENV.ORG_ID,
  };
}

export function graphql(query, variables) {
  const response = http.post(
    `${__ENV.BASE_URL}/graphql`,
    JSON.stringify({ query, variables }),
    { headers: correlationHeaders(), tags: { name: "graphql" } },
  );

  let payload = null;
  try {
    payload = response.json();
  } catch (_) {
    // A malformed response is recorded as a failed GraphQL request below.
  }

  const succeeded = response.status === 200 && !payload?.errors?.length;
  graphqlFailures.add(!succeeded);
  check(response, {
    "GraphQL HTTP 200": (result) => result.status === 200,
    "GraphQL has no errors": () => succeeded,
  });
  return { response, payload, succeeded };
}
