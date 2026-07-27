import exec from "k6/execution";
import { graphql } from "./lib.js";

const totalRequests = Number(__ENV.TOTAL_REQUESTS || 10000);
const targetRps = Number(__ENV.TARGET_RPS || 50);
const preAllocatedVUs = Number(__ENV.PRE_ALLOCATED_VUS || 20);
const maxVUs = Number(__ENV.MAX_VUS || 100);

if (!Number.isInteger(totalRequests) || totalRequests < 1) {
  throw new Error("TOTAL_REQUESTS must be a positive integer");
}
if (!Number.isInteger(targetRps) || targetRps < 1) {
  throw new Error("TARGET_RPS must be a positive integer");
}

const durationSeconds = Math.ceil(totalRequests / targetRps);

export const options = {
  scenarios: {
    fixed_request_count: {
      executor: "constant-arrival-rate",
      rate: targetRps,
      timeUnit: "1s",
      duration: `${durationSeconds}s`,
      preAllocatedVUs,
      maxVUs,
    },
  },
  thresholds: {
    graphql_failures: ["rate<0.01"],
    http_reqs: [`count==${totalRequests}`],
    http_req_duration: ["p(95)<2000"],
    dropped_iterations: ["count==0"],
  },
};

const query = `
  query FixedRequestCount($orgId: ID!) {
    folderTree(orgId: $orgId) { id name path }
  }
`;

export default function () {
  // The arrival-rate executor schedules a full final second. Surplus iterations
  // intentionally perform no I/O so TOTAL_REQUESTS remains exact.
  if (exec.scenario.iterationInTest >= totalRequests) return;
  graphql(query, { orgId: __ENV.ORG_ID });
}
