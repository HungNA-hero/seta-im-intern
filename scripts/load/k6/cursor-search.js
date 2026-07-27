import { sleep } from "k6";
import { graphql } from "./lib.js";

const maxVUs = Number(__ENV.MAX_VUS || 25);
const initialVUs = Math.min(5, maxVUs);
const rampUp = __ENV.RAMP_UP || "1m";
const holdDuration = __ENV.HOLD_DURATION || "3m";
const rampDown = __ENV.RAMP_DOWN || "1m";

export const options = {
  stages: [
    { duration: rampUp, target: initialVUs },
    { duration: holdDuration, target: maxVUs },
    { duration: rampDown, target: 0 },
  ],
  thresholds: {
    graphql_failures: ["rate<0.01"],
    http_req_duration: ["p(95)<2000"],
  },
};

const query = `
  query LoadCursorSearch($orgId: ID!, $folderId: ID!, $first: Int!) {
    searchMetadataConnection(
      orgId: $orgId
      input: { folderId: $folderId, first: $first }
    ) {
      nodes { id title updatedAt }
      pageInfo { endCursor hasNextPage }
    }
  }
`;

export function setup() {
  if (!__ENV.FOLDER_ID) {
    throw new Error("FOLDER_ID is required for cursor-search.js");
  }
}

export default function () {
  graphql(query, {
    orgId: __ENV.ORG_ID,
    folderId: __ENV.FOLDER_ID,
    first: Number(__ENV.PAGE_SIZE || 100),
  });
  sleep(Number(__ENV.SLEEP_SECONDS || 1));
}
