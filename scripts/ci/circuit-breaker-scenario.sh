#!/usr/bin/env bash
# Exercises the Access Core -> Asset Core breaker through deterministic Toxiproxy faults.
set -euo pipefail

require_command() {
    local command_name="$1"
    if command -v "$command_name" >/dev/null 2>&1; then
        return 0
    fi
    echo "Missing required command: $command_name" >&2
    exit 127
}

for command_name in awk curl docker git jq; do
    require_command "$command_name"
done
if ! docker compose version >/dev/null 2>&1; then
    echo "Docker Compose v2 is required (docker compose)." >&2
    exit 127
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
PROJECT_NAME="${COMPOSE_PROJECT_NAME:-seta-circuit-breaker-$$}"
export SETA_COMPOSE_PREFIX="${SETA_COMPOSE_PREFIX:-$PROJECT_NAME}"
export ASSET_DB_PORT="${ASSET_DB_PORT:-45433}"
export ACCESS_DB_PORT="${ACCESS_DB_PORT:-45434}"
export REDIS_PORT="${REDIS_PORT:-46379}"
export GO_ASSET_CORE_PORT="${GO_ASSET_CORE_PORT:-48080}"
export NODE_ACCESS_CORE_PORT="${NODE_ACCESS_CORE_PORT:-44000}"
export TOXIPROXY_API_PORT="${TOXIPROXY_API_PORT:-48474}"

COMPOSE=(
    docker compose
    --project-name "$PROJECT_NAME"
    --file "$REPO_ROOT/infra/docker-compose.yml"
    --file "$REPO_ROOT/infra/docker-compose.faultinject.yml"
)

ORG_ID="00000000-0000-0000-0000-000000000010"
ADMIN_ID="00000000-0000-0000-0000-000000000001"
VIEWER_ID="00000000-0000-0000-0000-000000000002"
DOGS_ID="10000000-0000-0000-0000-000000000002"
ACCESS_URL="http://127.0.0.1:${NODE_ACCESS_CORE_PORT}"
ASSET_URL="http://127.0.0.1:${GO_ASSET_CORE_PORT}"
TOXIPROXY_URL="http://127.0.0.1:${TOXIPROXY_API_PORT}"
TRIP_REQUESTS="${TRIP_REQUESTS:-12}"
HALF_OPEN_REQUESTS="${HALF_OPEN_REQUESTS:-12}"
HALF_OPEN_LATENCY_MS="${HALF_OPEN_LATENCY_MS:-1000}"
SHORT_CIRCUIT_LIMIT_MS="${SHORT_CIRCUIT_LIMIT_MS:-500}"

RUN_DIRECTORY="${CIRCUIT_BREAKER_RUN_DIRECTORY:-$REPO_ROOT/.cache/ci/circuit-breaker/$PROJECT_NAME}"
EVIDENCE_FILE="${CIRCUIT_BREAKER_EVIDENCE_FILE:-$RUN_DIRECTORY/evidence.json}"
RESPONSE_DIRECTORY="$RUN_DIRECTORY/responses"
DIAGNOSTICS_DIRECTORY="$RUN_DIRECTORY/diagnostics"
mkdir -p "$RESPONSE_DIRECTORY" "$DIAGNOSTICS_DIRECTORY" "$(dirname "$EVIDENCE_FILE")"

SCENARIO_STARTED_MS="$(date +%s%3N)"
SCENARIO_STARTED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
SCENARIO_STARTED=false
PATH_RESULTS='{}'
CURRENT_PATH=""
CURRENT_PATH_STARTED_MS=0
REQUEST_SEQUENCE=0

commit_sha="${GITHUB_SHA:-$(git -C "$REPO_ROOT" rev-parse HEAD)}"
ci_run_id="${GITHUB_RUN_ID:-local}"
ci_run_attempt="${GITHUB_RUN_ATTEMPT:-1}"
qualification_shard="${QUALIFICATION_SHARD:-candidate}"

record_path() {
    local name="$1"
    local status="$2"
    local started_ms="$3"
    local details="${4:-}"
    local finished_ms duration_ms
    if [[ -z "$details" ]]; then
        details='{}'
    fi
    finished_ms="$(date +%s%3N)"
    duration_ms=$((finished_ms - started_ms))
    PATH_RESULTS="$(
        jq --compact-output \
            --arg name "$name" \
            --arg status "$status" \
            --argjson duration_ms "$duration_ms" \
            --argjson details "$details" \
            '. + {($name): {
                status: $status,
                duration_ms: $duration_ms,
                details: $details
            }}' <<<"$PATH_RESULTS"
    )"
    CURRENT_PATH=""
}

run_path() {
    local name="$1"
    shift
    CURRENT_PATH="$name"
    CURRENT_PATH_STARTED_MS="$(date +%s%3N)"
    "$@"
}

write_evidence() {
    local exit_status="$1"
    local finished_ms duration_ms overall
    finished_ms="$(date +%s%3N)"
    duration_ms=$((finished_ms - SCENARIO_STARTED_MS))
    if [[ "$exit_status" -eq 0 ]]; then
        overall="passed"
    else
        overall="failed"
    fi

    jq --null-input \
        --arg schema_version "1" \
        --arg overall "$overall" \
        --arg commit_sha "$commit_sha" \
        --arg run_id "$ci_run_id" \
        --arg run_attempt "$ci_run_attempt" \
        --arg shard "$qualification_shard" \
        --arg project "$PROJECT_NAME" \
        --arg started_at "$SCENARIO_STARTED_AT" \
        --arg finished_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
        --argjson scenario_started "$SCENARIO_STARTED" \
        --argjson duration_ms "$duration_ms" \
        --argjson paths "$PATH_RESULTS" \
        '{
            schema_version: ($schema_version | tonumber),
            overall: $overall,
            commit_sha: $commit_sha,
            ci: {
                run_id: $run_id,
                run_attempt: ($run_attempt | tonumber),
                qualification_shard: $shard
            },
            compose_project: $project,
            scenario_started: $scenario_started,
            started_at: $started_at,
            finished_at: $finished_at,
            duration_ms: $duration_ms,
            paths: $paths
        }' >"$EVIDENCE_FILE"
    echo "Circuit-breaker evidence: $EVIDENCE_FILE"
}

collect_diagnostics() {
    "${COMPOSE[@]}" ps >"$DIAGNOSTICS_DIRECTORY/compose-ps.txt" 2>&1 || true
    "${COMPOSE[@]}" logs --no-color --tail 300 \
        access-core asset-core toxiproxy >"$DIAGNOSTICS_DIRECTORY/compose.log" 2>&1 || true
    curl --silent "$ACCESS_URL/metrics" \
        >"$DIAGNOSTICS_DIRECTORY/access-metrics.txt" 2>&1 || true
    curl --silent "$ASSET_URL/metrics" \
        >"$DIAGNOSTICS_DIRECTORY/asset-metrics.txt" 2>&1 || true
    curl --silent "$TOXIPROXY_URL/proxies/asset_core" \
        >"$DIAGNOSTICS_DIRECTORY/toxiproxy-proxy.json" 2>&1 || true
    curl --silent "$TOXIPROXY_URL/metrics" \
        >"$DIAGNOSTICS_DIRECTORY/toxiproxy-metrics.txt" 2>&1 || true
}

cleanup() {
    local status=$?
    trap - EXIT
    set +e
    if [[ "$status" -ne 0 ]]; then
        if [[ -n "$CURRENT_PATH" ]] \
            && ! jq --exit-status --arg name "$CURRENT_PATH" \
                'has($name)' >/dev/null 2>&1 <<<"$PATH_RESULTS"; then
            record_path \
                "$CURRENT_PATH" \
                "failed" \
                "$CURRENT_PATH_STARTED_MS" \
                '{"reason":"scenario command failed; see diagnostics"}'
        fi
        echo "Circuit-breaker scenario failed; collecting diagnostics." >&2
        collect_diagnostics
    fi
    curl --fail --silent --show-error --request POST \
        "$TOXIPROXY_URL/reset" >/dev/null 2>&1 || true
    "${COMPOSE[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true
    write_evidence "$status"
    exit "$status"
}
trap cleanup EXIT

wait_for_url() {
    local name="$1"
    local url="$2"
    for _ in {1..60}; do
        if curl --fail --silent "$url" >/dev/null 2>&1; then
            echo "$name is ready."
            return 0
        fi
        sleep 2
    done
    echo "$name did not become ready at $url" >&2
    return 1
}

access_metrics() {
    curl --fail --silent --show-error "$ACCESS_URL/metrics"
}

asset_metrics() {
    curl --fail --silent --show-error "$ASSET_URL/metrics"
}

breaker_state_value() {
    local state="$1"
    access_metrics | awk -v target="state=\"$state\"" '
        /^seta_access_asset_breaker_state\{/ && index($1, target) { print $2; found=1 }
        END { if (!found) print 0 }
    '
}

breaker_event_count() {
    local event="$1"
    access_metrics | awk -v target="event=\"$event\"" '
        /^seta_access_events_total\{/ && index($1, target) { print $2; found=1 }
        END { if (!found) print 0 }
    '
}

asset_folder_request_count() {
    asset_metrics | awk '
        /^seta_asset_http_requests_total\{/ &&
        index($1, "route=\"/internal/api/v1/folders\"") { total += $2 }
        END { print total + 0 }
    '
}

wait_for_breaker_state() {
    local expected="$1"
    for _ in {1..120}; do
        if [[ "$(breaker_state_value "$expected")" == "1" ]]; then
            echo "Breaker state $expected observed."
            return 0
        fi
        sleep 0.1
    done
    echo "Timed out waiting for breaker state $expected." >&2
    return 1
}

next_request_id() {
    REQUEST_SEQUENCE=$((REQUEST_SEQUENCE + 1))
    printf 'cb-%s-%s-%s-%s' \
        "$qualification_shard" "$BASHPID" "$(date +%s%N)" "$REQUEST_SEQUENCE"
}

graphql_request() {
    local user_id="$1"
    local payload="$2"
    local request_id
    request_id="$(next_request_id)"
    curl --fail --silent --show-error \
        --header "Content-Type: application/json" \
        --header "x-user-id: $user_id" \
        --header "x-org-id: $ORG_ID" \
        --header "x-request-id: $request_id" \
        --data "$payload" \
        "$ACCESS_URL/graphql"
}

folder_tree_request() {
    graphql_request "$ADMIN_ID" \
        "$(jq --compact-output --null-input --arg org_id "$ORG_ID" '{
            query: "query BreakerTree($orgId: ID!) { folderTree(orgId: $orgId) { id } }",
            variables: {orgId: $org_id}
        }')"
}

can_do_request() {
    graphql_request "$VIEWER_ID" \
        "$(jq --compact-output --null-input --arg resource_id "$DOGS_ID" '{
            query: "query BreakerCanDo($resourceId: ID!) { canDo(action: read, resourceType: folder, resourceId: $resourceId) { allowed reason } }",
            variables: {resourceId: $resource_id}
        }')"
}

assert_success_response() {
    local file="$1"
    jq --exit-status \
        '.errors == null and (.data.folderTree | type == "array")' \
        "$file" >/dev/null
}

assert_safe_internal_error() {
    local file="$1"
    jq --exit-status \
        'any(.errors[]?; .extensions.code == "INTERNAL_ERROR") and
         ([.. | objects | .allowed? // empty] | all(. != true))' \
        "$file" >/dev/null
}

run_parallel_folder_tree() {
    local count="$1"
    local prefix="$2"
    local -a pids=()
    local index
    for ((index = 1; index <= count; index++)); do
        folder_tree_request >"${prefix}-${index}.json" &
        pids+=("$!")
    done
    for pid in "${pids[@]}"; do
        wait "$pid"
    done
}

add_toxic() {
    local body="$1"
    curl --fail --silent --show-error \
        --request POST \
        --header "Content-Type: application/json" \
        --data "$body" \
        "$TOXIPROXY_URL/proxies/asset_core/toxics" >/dev/null
}

remove_toxic() {
    local name="$1"
    curl --fail --silent --show-error \
        --request DELETE \
        "$TOXIPROXY_URL/proxies/asset_core/toxics/$name" >/dev/null
}

baseline_closed() {
    local started_ms="$CURRENT_PATH_STARTED_MS"
    local response_file="$RESPONSE_DIRECTORY/baseline.json"
    local proxy
    proxy="$(curl --fail --silent --show-error "$TOXIPROXY_URL/proxies/asset_core")"
    jq --exit-status \
        '.name == "asset_core" and
         (.listen == "0.0.0.0:18080" or .listen == "[::]:18080") and
         .upstream == "asset-core:8080" and .enabled == true and
         (.toxics | length) == 0' >/dev/null <<<"$proxy"
    [[ "$(breaker_state_value closed)" == "1" ]]
    folder_tree_request >"$response_file"
    assert_success_response "$response_file"
    record_path \
        "baseline_closed" \
        "passed" \
        "$started_ms" \
        "$(jq --null-input --argjson asset_requests "$(asset_folder_request_count)" \
            '{breaker_state:"closed", asset_folder_requests:$asset_requests}')"
}

timeout_trip() {
    local started_ms="$CURRENT_PATH_STARTED_MS"
    local prefix="$RESPONSE_DIRECTORY/trip"
    local before_requests after_requests index open_count
    before_requests="$(asset_folder_request_count)"
    add_toxic \
        '{"name":"timeout_downstream","type":"timeout","stream":"downstream","toxicity":1,"attributes":{"timeout":0}}'
    run_parallel_folder_tree "$TRIP_REQUESTS" "$prefix"
    for ((index = 1; index <= TRIP_REQUESTS; index++)); do
        assert_safe_internal_error "${prefix}-${index}.json"
    done
    wait_for_breaker_state open
    open_count="$(breaker_event_count asset_breaker_open)"
    (( open_count >= 1 ))
    after_requests="$(asset_folder_request_count)"
    (( after_requests - before_requests >= 10 ))
    record_path \
        "timeout_trip" \
        "passed" \
        "$started_ms" \
        "$(jq --null-input \
            --argjson submitted "$TRIP_REQUESTS" \
            --argjson asset_request_delta "$((after_requests - before_requests))" \
            --argjson open_events "$open_count" \
            '{submitted:$submitted, safe_internal_errors:$submitted,
              asset_request_delta:$asset_request_delta, open_events:$open_events}')"
}

open_fail_closed_no_io() {
    local started_ms="$CURRENT_PATH_STARTED_MS"
    local response_file="$RESPONSE_DIRECTORY/open-can-do.json"
    local before_requests after_requests request_started request_finished elapsed_ms rejects
    before_requests="$(asset_folder_request_count)"
    request_started="$(date +%s%3N)"
    can_do_request >"$response_file"
    request_finished="$(date +%s%3N)"
    elapsed_ms=$((request_finished - request_started))
    assert_safe_internal_error "$response_file"
    after_requests="$(asset_folder_request_count)"
    [[ "$after_requests" == "$before_requests" ]]
    (( elapsed_ms < SHORT_CIRCUIT_LIMIT_MS ))
    rejects="$(breaker_event_count asset_breaker_reject)"
    (( rejects >= 1 ))
    record_path \
        "open_fail_closed_no_io" \
        "passed" \
        "$started_ms" \
        "$(jq --null-input \
            --argjson elapsed_ms "$elapsed_ms" \
            --argjson asset_request_delta "$((after_requests - before_requests))" \
            --argjson reject_events "$rejects" \
            '{elapsed_ms:$elapsed_ms, latency_limit_ms:'"$SHORT_CIRCUIT_LIMIT_MS"',
              asset_request_delta:$asset_request_delta, never_allowed_true:true,
              reject_events:$reject_events}')"
}

half_open_single_probe() {
    local started_ms="$CURRENT_PATH_STARTED_MS"
    local prefix="$RESPONSE_DIRECTORY/half-open"
    local before_requests after_requests index successes=0 failures=0
    local half_open_count close_count
    remove_toxic timeout_downstream
    add_toxic \
        '{"name":"latency_downstream","type":"latency","stream":"downstream","toxicity":1,"attributes":{"latency":'"$HALF_OPEN_LATENCY_MS"',"jitter":0}}'
    wait_for_breaker_state halfOpen
    before_requests="$(asset_folder_request_count)"
    run_parallel_folder_tree "$HALF_OPEN_REQUESTS" "$prefix"
    for ((index = 1; index <= HALF_OPEN_REQUESTS; index++)); do
        if assert_success_response "${prefix}-${index}.json"; then
            successes=$((successes + 1))
        elif assert_safe_internal_error "${prefix}-${index}.json"; then
            failures=$((failures + 1))
        else
            echo "Unexpected half-open response: ${prefix}-${index}.json" >&2
            return 1
        fi
    done
    [[ "$successes" == "1" ]]
    [[ "$failures" == "$((HALF_OPEN_REQUESTS - 1))" ]]
    after_requests="$(asset_folder_request_count)"
    [[ "$((after_requests - before_requests))" == "1" ]]
    wait_for_breaker_state closed
    half_open_count="$(breaker_event_count asset_breaker_half_open)"
    close_count="$(breaker_event_count asset_breaker_close)"
    (( half_open_count >= 1 && close_count >= 1 ))
    record_path \
        "half_open_single_probe" \
        "passed" \
        "$started_ms" \
        "$(jq --null-input \
            --argjson submitted "$HALF_OPEN_REQUESTS" \
            --argjson successes "$successes" \
            --argjson fail_closed "$failures" \
            --argjson asset_request_delta "$((after_requests - before_requests))" \
            --argjson half_open_events "$half_open_count" \
            --argjson close_events "$close_count" \
            '{submitted:$submitted, successes:$successes, fail_closed:$fail_closed,
              asset_request_delta:$asset_request_delta,
              half_open_events:$half_open_events, close_events:$close_events}')"
}

recovery_closed() {
    local started_ms="$CURRENT_PATH_STARTED_MS"
    local response_file="$RESPONSE_DIRECTORY/recovery.json"
    local before_requests after_requests
    remove_toxic latency_downstream
    before_requests="$(asset_folder_request_count)"
    folder_tree_request >"$response_file"
    assert_success_response "$response_file"
    after_requests="$(asset_folder_request_count)"
    [[ "$((after_requests - before_requests))" == "1" ]]
    [[ "$(breaker_state_value closed)" == "1" ]]
    record_path \
        "recovery_closed" \
        "passed" \
        "$started_ms" \
        "$(jq --null-input --argjson asset_request_delta "$((after_requests - before_requests))" \
            '{breaker_state:"closed", asset_request_delta:$asset_request_delta}')"
}

post_recovery_fresh_window() {
    local started_ms="$CURRENT_PATH_STARTED_MS"
    local response_file="$RESPONSE_DIRECTORY/post-recovery-failure.json"
    local open_events_before open_events_after
    open_events_before="$(breaker_event_count asset_breaker_open)"
    add_toxic \
        '{"name":"reset_peer_downstream","type":"reset_peer","stream":"downstream","toxicity":1,"attributes":{"timeout":0}}'
    folder_tree_request >"$response_file"
    assert_safe_internal_error "$response_file"
    [[ "$(breaker_state_value closed)" == "1" ]]
    open_events_after="$(breaker_event_count asset_breaker_open)"
    [[ "$open_events_after" == "$open_events_before" ]]
    record_path \
        "post_recovery_fresh_window" \
        "passed" \
        "$started_ms" \
        "$(jq --null-input \
            --argjson open_events_before "$open_events_before" \
            --argjson open_events_after "$open_events_after" \
            '{safe_internal_error:true, breaker_state:"closed",
              open_events_before:$open_events_before,
              open_events_after:$open_events_after}')"
}

toxiproxy_reset_cleanup() {
    local started_ms="$CURRENT_PATH_STARTED_MS"
    local response_file="$RESPONSE_DIRECTORY/final-healthy.json"
    curl --fail --silent --show-error --request POST \
        "$TOXIPROXY_URL/reset" >/dev/null
    jq --exit-status '.toxics | length == 0' >/dev/null \
        <<<"$(curl --fail --silent --show-error "$TOXIPROXY_URL/proxies/asset_core")"
    folder_tree_request >"$response_file"
    assert_success_response "$response_file"
    [[ "$(breaker_state_value closed)" == "1" ]]
    record_path \
        "toxiproxy_reset_cleanup" \
        "passed" \
        "$started_ms" \
        '{"toxics_remaining":0,"final_request":"healthy","breaker_state":"closed"}'
}

cd "$REPO_ROOT"
"${COMPOSE[@]}" config --quiet
"${COMPOSE[@]}" build asset-core access-core
"${COMPOSE[@]}" up --detach asset-db access-db redis
"${COMPOSE[@]}" --profile migration run --rm flyway-asset
"${COMPOSE[@]}" --profile migration run --rm flyway-access
"${COMPOSE[@]}" exec --no-TTY access-db \
    psql -U access_user -d access_db <"$REPO_ROOT/infra/db/access/seed/demo_fixtures.sql"
"${COMPOSE[@]}" exec --no-TTY access-db \
    psql -U access_user -d access_db \
    --command "UPDATE access.organizations SET olp_enabled = true WHERE id = '$ORG_ID';" >/dev/null
"${COMPOSE[@]}" exec --no-TTY asset-db \
    psql -U asset_user -d asset_db <"$REPO_ROOT/infra/db/asset/seed/demo_fixtures.sql"
"${COMPOSE[@]}" up --detach asset-core toxiproxy

wait_for_url "Asset Core" "$ASSET_URL/healthz"
wait_for_url "Toxiproxy" "$TOXIPROXY_URL/version"
"${COMPOSE[@]}" up --detach access-core
wait_for_url "Access Core" "$ACCESS_URL/health"
wait_for_url "Access Core metrics" "$ACCESS_URL/metrics"

SCENARIO_STARTED=true
run_path baseline_closed baseline_closed
run_path timeout_trip timeout_trip
run_path open_fail_closed_no_io open_fail_closed_no_io
run_path half_open_single_probe half_open_single_probe
run_path recovery_closed recovery_closed
run_path post_recovery_fresh_window post_recovery_fresh_window
run_path toxiproxy_reset_cleanup toxiproxy_reset_cleanup

echo "Disposable circuit-breaker fault scenario completed successfully."
