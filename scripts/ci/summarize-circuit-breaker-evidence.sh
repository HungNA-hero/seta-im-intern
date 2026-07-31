#!/usr/bin/env bash
# Qualifies a collection of isolated circuit-breaker CI evidence reports.
set -euo pipefail

EVIDENCE_DIRECTORY="${1:-.cache/qualification/evidence}"
SUMMARY_FILE="${CIRCUIT_BREAKER_QUALIFICATION_SUMMARY:-.cache/qualification/summary.json}"
EXPECTED_RUNS="${EXPECTED_QUALIFICATION_RUNS:-20}"
P95_BUDGET_MS="${CIRCUIT_BREAKER_P95_BUDGET_MS:-300000}"
REQUIRED_PATHS_JSON='[
  "baseline_closed",
  "timeout_trip",
  "open_fail_closed_no_io",
  "half_open_single_probe",
  "recovery_closed",
  "post_recovery_fresh_window",
  "toxiproxy_reset_cleanup"
]'

for command_name in find jq sort; do
    if ! command -v "$command_name" >/dev/null 2>&1; then
        echo "Missing required command: $command_name" >&2
        exit 127
    fi
done

mkdir -p "$(dirname "$SUMMARY_FILE")"
mapfile -d '' evidence_files < <(
    find "$EVIDENCE_DIRECTORY" -type f -name 'evidence*.json' -print0 | sort -z
)

qualification_reason=""
qualified=true
report_count="${#evidence_files[@]}"
reports='[]'

if [[ "$report_count" -eq 0 ]]; then
    qualified=false
    qualification_reason="no evidence reports found"
elif ! reports="$(jq --slurp '.' "${evidence_files[@]}")"; then
    qualified=false
    qualification_reason="one or more evidence reports are malformed"
    reports='[]'
fi

if [[ "$qualified" == true ]] && [[ "$report_count" -ne "$EXPECTED_RUNS" ]]; then
    qualified=false
    qualification_reason="expected $EXPECTED_RUNS reports, found $report_count"
fi

if [[ "$qualified" == true ]] && ! jq --exit-status \
    --argjson required_paths "$REQUIRED_PATHS_JSON" \
    'all(.[];
        . as $report |
        $report.schema_version == 1 and
        $report.scenario_started == true and
        $report.overall == "passed" and
        $report.ci.run_attempt == 1 and
        ($report.duration_ms | type == "number") and
        all($required_paths[];
            . as $path |
            $report.paths[$path].status == "passed" and
            ($report.paths[$path].duration_ms | type == "number")
        )
    )' >/dev/null <<<"$reports"; then
    qualified=false
    qualification_reason="a report is failed, retried, incomplete, or missing a required path"
fi

if [[ "$qualified" == true ]] && ! jq --exit-status \
    --argjson expected "$EXPECTED_RUNS" \
    '([.[].ci.qualification_shard] | unique | length) == $expected and
     ([.[].commit_sha] | unique | length) == 1 and
     (.[0].commit_sha | length) > 0' >/dev/null <<<"$reports"; then
    qualified=false
    qualification_reason="reports do not contain unique shards for one commit"
fi

p95_runtime_ms=0
if [[ "$report_count" -gt 0 ]] && [[ "$(jq 'length' <<<"$reports")" -gt 0 ]]; then
    p95_runtime_ms="$(
        jq '
            [.[].duration_ms] | sort |
            .[((length * 0.95 | ceil) - 1)]
        ' <<<"$reports"
    )"
fi

if [[ "$qualified" == true ]] && (( p95_runtime_ms > P95_BUDGET_MS )); then
    qualified=false
    qualification_reason="p95 runtime ${p95_runtime_ms}ms exceeds ${P95_BUDGET_MS}ms"
fi

path_totals="$(
    jq --argjson required_paths "$REQUIRED_PATHS_JSON" '
        . as $reports |
        reduce $required_paths[] as $path ({};
            .[$path] = {
                passed: ([$reports[] | select(.paths[$path].status == "passed")] | length),
                failed: ([$reports[] | select(.paths[$path].status != "passed")] | length)
            }
        )
    ' <<<"$reports"
)"

tested_commit="$(jq --raw-output '.[0].commit_sha // ""' <<<"$reports")"
jq --null-input \
    --argjson qualified "$qualified" \
    --arg reason "$qualification_reason" \
    --arg tested_commit "$tested_commit" \
    --argjson expected_runs "$EXPECTED_RUNS" \
    --argjson report_count "$report_count" \
    --argjson p95_runtime_ms "$p95_runtime_ms" \
    --argjson p95_budget_ms "$P95_BUDGET_MS" \
    --argjson required_paths "$REQUIRED_PATHS_JSON" \
    --argjson path_totals "$path_totals" \
    '{
        qualified: $qualified,
        reason: (if $qualified then null else $reason end),
        tested_commit: $tested_commit,
        expected_runs: $expected_runs,
        report_count: $report_count,
        p95_runtime_ms: $p95_runtime_ms,
        p95_budget_ms: $p95_budget_ms,
        required_paths: $required_paths,
        path_totals: $path_totals
    }' >"$SUMMARY_FILE"

cat "$SUMMARY_FILE"
if [[ "$qualified" != true ]]; then
    exit 1
fi
