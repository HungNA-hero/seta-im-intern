import { sleep } from "k6";
import { graphql } from "./lib.js";

const totalRequests = Number(__ENV.TOTAL_REQUESTS || 10000);
const targetRps = Number(__ENV.TARGET_RPS || 50);
const durationSeconds = Math.ceil(totalRequests / targetRps);
const preAllocatedVUs = Number(__ENV.PRE_ALLOCATED_VUS || 20);
const maxVUs = Number(__ENV.MAX_VUS || 100);

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
    http_req_duration: ["p(95)<2000"],
    dropped_iterations: ["count==0"],
  },
};

const query = `
  query FixedRequestCount($orgId: ID!) {
    folderTree(orgId: $orgId) { id name path }
  }
`;

export function setup() {
  if (!Number.isInteger(totalRequests) || totalRequests < 1) {
    throw new Error("TOTAL_REQUESTS must be a positive integer");
  }
  if (!Number.isFinite(targetRps) || targetRps <= 0) {
    throw new Error("TARGET_RPS must be a positive number");
  }
}

export default function () {
  graphql(query, { orgId: __ENV.ORG_ID });
  sleep(0);
}
