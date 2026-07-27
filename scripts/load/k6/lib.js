import http from "k6/http";
import { check } from "k6";
import { Rate } from "k6/metrics";

export const graphqlFailures = new Rate("graphql_failures");

const runId = __ENV.RUN_ID;
if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(runId || "")) {
  throw new Error(
    "RUN_ID is required and must use 1-64 letters, digits, dots, underscores, or hyphens",
  );
}

function fixedWidthHex(value, width) {
  return Math.max(0, value).toString(16).padStart(width, "0").slice(-width);
}

function hash32(value, seed) {
  let hash = seed >>> 0;
  for (let index = 0; index < value.length; index += 1) {
    hash = Math.imul(hash ^ value.charCodeAt(index), 16777619) >>> 0;
  }
  return fixedWidthHex(hash, 8);
}

export function correlationHeaders() {
  const runHash = `${hash32(runId, 2166136261)}${hash32(runId, 2246822519)}`;
  const traceId = `${runHash}${fixedWidthHex(__VU, 8)}${fixedWidthHex(__ITER + 1, 8)}`;
  return {
    "Content-Type": "application/json",
    traceparent: `00-${traceId}-${fixedWidthHex(__ITER + 1, 16)}-01`,
    "x-request-id": `k6-${runId}-${__VU}-${__ITER}`,
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
