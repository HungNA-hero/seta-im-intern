import { check, sleep } from "k6";
import { Counter, Rate } from "k6/metrics";
import { correlationHeaders } from "./lib.js";
import http from "k6/http";

const safeInternalErrors = new Counter("safe_internal_errors");
const recoveredResponses = new Counter("recovered_responses");
const transportFailures = new Rate("transport_failures");
const maxVUs = Number(__ENV.MAX_VUS || 20);
const initialVUs = Math.min(5, maxVUs);
const rampUp = __ENV.RAMP_UP || "1m";
const holdDuration = __ENV.HOLD_DURATION || "5m";
const rampDown = __ENV.RAMP_DOWN || "1m";

export const options = {
  stages: [
    { duration: rampUp, target: initialVUs },
    { duration: holdDuration, target: maxVUs },
    { duration: rampDown, target: 0 },
  ],
  thresholds: {
    safe_internal_errors: ["count>0"],
    recovered_responses: ["count>0"],
    transport_failures: ["rate==0"],
    http_req_duration: ["p(99)<3500"],
  },
};

const query = `
  query BreakerProbe($orgId: ID!) {
    folderTree(orgId: $orgId) { id }
  }
`;

let sawSafeInternalError = false;

export default function () {
  const response = http.post(
    `${__ENV.BASE_URL}/graphql`,
    JSON.stringify({ query, variables: { orgId: __ENV.ORG_ID } }),
    { headers: correlationHeaders(), tags: { name: "breaker-probe" } },
  );
  let payload = null;
  try {
    payload = response.json();
  } catch (_) {
    // The check below captures transport failures without leaking raw output.
  }

  const safeInternalError = payload?.errors?.some(
    (error) => error?.extensions?.code === "INTERNAL_ERROR",
  );
  const succeeded =
    response.status === 200 &&
    Boolean(payload?.data) &&
    !payload?.errors?.length;
  if (safeInternalError) {
    safeInternalErrors.add(1);
    sawSafeInternalError = true;
  } else if (succeeded && sawSafeInternalError) {
    recoveredResponses.add(1);
    sawSafeInternalError = false;
  }
  transportFailures.add(response.status !== 200);
  check(response, {
    "GraphQL transport remains available": (result) => result.status === 200,
    "response is success or a safe internal error": () =>
      succeeded || safeInternalError,
  });
  sleep(Number(__ENV.SLEEP_SECONDS || 0.25));
}
