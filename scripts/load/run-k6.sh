#!/usr/bin/env bash
# Run a k6 scenario in Docker from WSL2 or a native Linux host.

set -euo pipefail

usage() {
  cat <<'EOF'
Usage: bash scripts/load/run-k6.sh [options]

Options:
  --scenario NAME          read-folder-tree | cursor-search | breaker-recovery | fixed-request-count
  --base-url URL           Host-side Access Core URL for the health check
  --k6-base-url URL        Access Core URL as seen from the k6 container
  --user-id UUID           Seeded caller UUID
  --org-id UUID            Seeded organization UUID
  --folder-id UUID         Folder UUID for cursor-search
  --max-vus NUMBER         Maximum virtual users
  --sleep-seconds NUMBER   Think time between iterations
  --ramp-up DURATION       k6 ramp-up duration, for example 15s
  --hold-duration DURATION k6 hold duration, for example 30s
  --ramp-down DURATION     k6 ramp-down duration, for example 15s
  --page-size NUMBER       Cursor-search page size
  --total-requests NUMBER  Fixed-count request total
  --target-rps NUMBER      Fixed-count target request rate
  --pre-allocated-vus NUM  Fixed-count pre-allocated VUs
  --help                   Show this help
EOF
}

scenario="read-folder-tree"
base_url="http://localhost:4000"
k6_base_url="http://host.docker.internal:4000"
user_id="00000000-0000-0000-0000-000000000001"
org_id="00000000-0000-0000-0000-000000000010"
folder_id="10000000-0000-0000-0000-000000000002"
max_vus="25"
sleep_seconds="1"
ramp_up="1m"
hold_duration="3m"
ramp_down="1m"
page_size="100"
total_requests="10000"
target_rps="50"
pre_allocated_vus="20"

while (($# > 0)); do
  case "$1" in
    --scenario|--base-url|--k6-base-url|--user-id|--org-id|--folder-id|--max-vus|--sleep-seconds|--ramp-up|--hold-duration|--ramp-down|--page-size|--total-requests|--target-rps|--pre-allocated-vus)
      if (($# < 2)); then
        echo "Missing value for $1" >&2
        usage >&2
        exit 2
      fi
      case "$1" in
        --scenario) scenario="$2" ;;
        --base-url) base_url="$2" ;;
        --k6-base-url) k6_base_url="$2" ;;
        --user-id) user_id="$2" ;;
        --org-id) org_id="$2" ;;
        --folder-id) folder_id="$2" ;;
        --max-vus) max_vus="$2" ;;
        --sleep-seconds) sleep_seconds="$2" ;;
        --ramp-up) ramp_up="$2" ;;
        --hold-duration) hold_duration="$2" ;;
        --ramp-down) ramp_down="$2" ;;
        --page-size) page_size="$2" ;;
        --total-requests) total_requests="$2" ;;
        --target-rps) target_rps="$2" ;;
        --pre-allocated-vus) pre_allocated_vus="$2" ;;
      esac
      shift 2
      ;;
    --help|-h) usage; exit 0 ;;
    *) echo "Unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

case "$scenario" in
  read-folder-tree|cursor-search|breaker-recovery|fixed-request-count) ;;
  *) echo "Unsupported scenario: $scenario" >&2; usage >&2; exit 2 ;;
esac

for command in docker curl; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "$command is required." >&2
    exit 1
  fi
done

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
scenario_path="/scripts/scripts/load/k6/${scenario}.js"
results_dir="${repo_root}/.cache/load"
mkdir -p "$results_dir"

if ! curl --fail --silent --show-error --connect-timeout 5 "${base_url}/health" >/dev/null; then
  echo "Access Core is not healthy at ${base_url}/health. Start the local stack before load testing." >&2
  exit 1
fi

timestamp="$(date +%Y%m%d-%H%M%S)"
summary_file="/results/${timestamp}-${scenario}-summary.json"

echo "Running ${scenario} against ${k6_base_url} with at most ${max_vus} VUs."
docker run --rm -i \
  --add-host=host.docker.internal:host-gateway \
  -v "${repo_root}:/scripts:ro" \
  -v "${results_dir}:/results" \
  -e "BASE_URL=${k6_base_url}" \
  -e "USER_ID=${user_id}" \
  -e "ORG_ID=${org_id}" \
  -e "FOLDER_ID=${folder_id}" \
  -e "MAX_VUS=${max_vus}" \
  -e "SLEEP_SECONDS=${sleep_seconds}" \
  -e "RAMP_UP=${ramp_up}" \
  -e "HOLD_DURATION=${hold_duration}" \
  -e "RAMP_DOWN=${ramp_down}" \
  -e "PAGE_SIZE=${page_size}" \
  -e "TOTAL_REQUESTS=${total_requests}" \
  -e "TARGET_RPS=${target_rps}" \
  -e "PRE_ALLOCATED_VUS=${pre_allocated_vus}" \
  grafana/k6:0.55.0 run --summary-export "$summary_file" "$scenario_path"

echo "k6 summary saved under ${results_dir}."
