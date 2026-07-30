#!/usr/bin/env bash
# Proves that a bounded, disposable lifecycle-event backlog drains after Access Core returns.
set -euo pipefail

require_command() {
    local command_name="$1"
    if command -v "$command_name" >/dev/null 2>&1; then
        return 0
    fi

    echo "Missing required command: $command_name" >&2
    if [[ "$command_name" == "jq" ]]; then
        echo "On Ubuntu/Debian: sudo apt-get install -y jq" >&2
    fi
    exit 127
}

for command_name in docker curl jq; do
    require_command "$command_name"
done

if ! docker compose version >/dev/null 2>&1; then
    echo "Docker Compose v2 is required (docker compose)." >&2
    exit 127
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
PROJECT_NAME="${COMPOSE_PROJECT_NAME:-seta-event-backlog-$$}"
export SETA_COMPOSE_PREFIX="${SETA_COMPOSE_PREFIX:-$PROJECT_NAME}"
export ASSET_DB_PORT="${ASSET_DB_PORT:-35433}"
export ACCESS_DB_PORT="${ACCESS_DB_PORT:-35434}"
export REDIS_PORT="${REDIS_PORT:-36379}"
export GO_ASSET_CORE_PORT="${GO_ASSET_CORE_PORT:-38080}"
export NODE_ACCESS_CORE_PORT="${NODE_ACCESS_CORE_PORT:-34000}"
export PROMETHEUS_PORT="${PROMETHEUS_PORT:-39090}"
export REDIS_EXPORTER_PORT="${REDIS_EXPORTER_PORT:-39121}"
export GRAFANA_PORT="${GRAFANA_PORT:-33000}"
export LOKI_PORT="${LOKI_PORT:-33100}"
export ALLOY_PORT="${ALLOY_PORT:-32345}"

COMPOSE=(
    docker compose
    --project-name "$PROJECT_NAME"
    --file "$REPO_ROOT/infra/docker-compose.yml"
    --file "$REPO_ROOT/infra/docker-compose.observability.yml"
    --profile observability
)

ORG_ID="00000000-0000-0000-0000-000000000010"
USER_ID="00000000-0000-0000-0000-000000000001"
ROOT_ID="10000000-0000-0000-0000-000000000000"
ANIMALS_ID="10000000-0000-0000-0000-000000000001"
DOGS_ID="10000000-0000-0000-0000-000000000002"
ASSET_TOKEN="${ASSET_INTERNAL_API_TOKEN:-seta-dam-local-development-token}"
ASSET_URL="http://127.0.0.1:${GO_ASSET_CORE_PORT}"
ACCESS_URL="http://127.0.0.1:${NODE_ACCESS_CORE_PORT}"
PROMETHEUS_URL="http://127.0.0.1:${PROMETHEUS_PORT}"
STREAM_KEY="stream:asset-events"
CONSUMER_GROUP="cache-invalidator"
BATCH_SIZE=12
RECOVERY_TIMEOUT_SECONDS=60

cleanup() {
    status=$?
    if [[ "$status" -ne 0 ]]; then
        echo "Event-backlog scenario failed; collecting diagnostics." >&2
        "${COMPOSE[@]}" ps || true
        "${COMPOSE[@]}" logs --no-color --tail 200 \
            asset-core access-core redis redis-exporter prometheus || true
    fi
    "${COMPOSE[@]}" down --volumes --remove-orphans || true
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

prom_query() {
    local query="$1"
    curl --fail --silent --get "$PROMETHEUS_URL/api/v1/query" \
        --data-urlencode "query=$query"
}

prom_count_equals_one() {
    local query="$1"
    jq --exit-status \
        '.status == "success" and (.data.result | length) == 1 and
         (.data.result[0].value[1] | tonumber) == 1' \
        >/dev/null <<<"$(prom_query "$query")"
}

wait_for_prom_value() {
    local description="$1"
    local query="$2"
    local minimum="$3"
    local response
    for _ in {1..45}; do
        response="$(prom_query "$query")"
        if jq --exit-status --argjson minimum "$minimum" \
            '.status == "success" and
             (.data.result | length) > 0 and
             ((.data.result[0].value[1] | tonumber) >= $minimum)' \
            >/dev/null <<<"$response"; then
            echo "$description observed."
            return 0
        fi
        sleep 2
    done
    echo "Timed out waiting for $description; last response: $response" >&2
    return 1
}

read_asset_epoch() {
    local value
    value="$("${COMPOSE[@]}" exec --no-TTY redis \
        redis-cli --raw GET "epoch:asset:$ORG_ID" | tr -d '\r')"
    echo "${value:-0}"
}

prepare_stale_pending_entries() {
    local -a pending_fields
    local reclaim_message_id
    local exhausted_message_id

    "${COMPOSE[@]}" exec --no-TTY redis \
        redis-cli --raw XREADGROUP GROUP "$CONSUMER_GROUP" scenario-precondition COUNT 2 \
        STREAMS "$STREAM_KEY" ">" >/dev/null

    mapfile -t pending_fields < <(
        "${COMPOSE[@]}" exec --no-TTY redis \
            redis-cli --raw XPENDING "$STREAM_KEY" "$CONSUMER_GROUP" - + 10
    )
    if (( ${#pending_fields[@]} < 8 )); then
        echo "Expected two pending entries but found ${#pending_fields[@]} pending fields." >&2
        return 1
    fi

    reclaim_message_id="${pending_fields[0]}"
    exhausted_message_id="${pending_fields[4]}"

    "${COMPOSE[@]}" exec --no-TTY redis \
        redis-cli XCLAIM "$STREAM_KEY" "$CONSUMER_GROUP" scenario-precondition 0 \
        "$reclaim_message_id" IDLE 31000 RETRYCOUNT 1 >/dev/null
    "${COMPOSE[@]}" exec --no-TTY redis \
        redis-cli XCLAIM "$STREAM_KEY" "$CONSUMER_GROUP" scenario-precondition 0 \
        "$exhausted_message_id" IDLE 31000 RETRYCOUNT 5 >/dev/null

    echo "Prepared one stale retryable entry and one stale retry-exhausted entry."
}

move_dogs() {
    local destination_id="$1"
    local response
    response="$(
        curl --fail --silent --request PATCH \
            "$ASSET_URL/internal/api/v1/folders/move?id=$DOGS_ID&orgId=$ORG_ID" \
            --header "Authorization: Bearer $ASSET_TOKEN" \
            --header "Content-Type: application/json" \
            --header "X-User-Id: $USER_ID" \
            --header "X-Org-Id: $ORG_ID" \
            --data "{\"destination_parent_id\":\"$destination_id\"}"
    )"
    jq --exit-status '.status == "success"' >/dev/null <<<"$response"
}

wait_for_recovery() {
    local baseline_epoch="$1"
    local expected_epoch=$((baseline_epoch + BATCH_SIZE))
    local started_at
    local current_epoch
    started_at="$(date +%s)"

    for _ in {1..30}; do
        current_epoch="$(read_asset_epoch)"
        if jq --exit-status \
            '.status == "success" and (.data.result | length) == 1 and
             (.data.result[0].value[1] | tonumber) == 1' \
            >/dev/null <<<"$(prom_query 'count(seta_asset_event_consumer_group_lag == 0)')" \
            && jq --exit-status \
                '.status == "success" and (.data.result | length) == 1 and
                 (.data.result[0].value[1] | tonumber) == 1' \
                >/dev/null <<<"$(prom_query 'count(seta_asset_event_consumer_group_pending == 0)')" \
            && [[ "$current_epoch" == "$expected_epoch" ]]; then
            echo "Backlog recovery completed in $(( $(date +%s) - started_at ))s; epoch=$current_epoch."
            return 0
        fi
        sleep 2
    done

    echo "Backlog did not drain in ${RECOVERY_TIMEOUT_SECONDS}s." >&2
    echo "Expected epoch=$expected_epoch; actual epoch=$current_epoch." >&2
    prom_query 'seta_asset_event_consumer_group_lag' >&2 || true
    prom_query 'seta_asset_event_consumer_group_pending' >&2 || true
    return 1
}

wait_for_reclaim_and_dlq() {
    local baseline_epoch="$1"
    local expected_epoch=$((baseline_epoch + 1))
    local started_at
    local current_epoch
    started_at="$(date +%s)"

    for _ in {1..30}; do
        current_epoch="$(read_asset_epoch)"
        if prom_count_equals_one 'count(seta_asset_event_consumer_group_lag == 0)' \
            && prom_count_equals_one 'count(seta_asset_event_consumer_group_pending == 0)' \
            && prom_count_equals_one 'count(seta_asset_event_dlq_depth == 1)' \
            && [[ "$current_epoch" == "$expected_epoch" ]] \
            && "${COMPOSE[@]}" logs --no-color access-core | grep -q '"event":"cache_invalidator_dlq"'; then
            echo "Pending reclaim and DLQ handling completed in $(( $(date +%s) - started_at ))s; epoch=$current_epoch; dlqDepth=1."
            return 0
        fi
        sleep 2
    done

    echo "Pending reclaim and DLQ handling did not complete in ${RECOVERY_TIMEOUT_SECONDS}s." >&2
    echo "Expected epoch=$expected_epoch; actual epoch=$current_epoch." >&2
    prom_query 'seta_asset_event_consumer_group_lag' >&2 || true
    prom_query 'seta_asset_event_consumer_group_pending' >&2 || true
    prom_query 'seta_asset_event_dlq_depth' >&2 || true
    return 1
}

cd "$REPO_ROOT"
"${COMPOSE[@]}" config --quiet
"${COMPOSE[@]}" build asset-core access-core
"${COMPOSE[@]}" up --detach asset-db access-db redis redis-exporter
"${COMPOSE[@]}" --profile migration run --rm flyway-asset
"${COMPOSE[@]}" --profile migration run --rm flyway-access
"${COMPOSE[@]}" exec --no-TTY access-db \
    psql -U access_user -d access_db < "$REPO_ROOT/infra/db/access/seed/demo_fixtures.sql"
"${COMPOSE[@]}" exec --no-TTY asset-db \
    psql -U asset_user -d asset_db < "$REPO_ROOT/infra/db/asset/seed/demo_fixtures.sql"
"${COMPOSE[@]}" up --detach asset-core access-core prometheus

wait_for_url "Asset Core" "$ASSET_URL/healthz"
wait_for_url "Access Core" "$ACCESS_URL/health"
wait_for_url "Prometheus" "$PROMETHEUS_URL/-/ready"
wait_for_prom_value \
    "healthy independent Redis observer" \
    "seta_asset_event_stream_observer_up" \
    1
wait_for_prom_value \
    "cache-invalidator consumer group" \
    "seta_asset_event_consumer_group_present" \
    1

baseline_epoch="$(read_asset_epoch)"
"${COMPOSE[@]}" stop access-core

for ((index = 0; index < BATCH_SIZE; index += 1)); do
    if ((index % 2 == 0)); then
        move_dogs "$ROOT_ID"
    else
        move_dogs "$ANIMALS_ID"
    fi
done

wait_for_prom_value \
    "consumer-group lag for $BATCH_SIZE real lifecycle events" \
    "seta_asset_event_consumer_group_lag" \
    "$BATCH_SIZE"

"${COMPOSE[@]}" up --detach access-core
wait_for_url "restarted Access Core" "$ACCESS_URL/health"
wait_for_recovery "$baseline_epoch"

reclaim_baseline_epoch="$(read_asset_epoch)"
"${COMPOSE[@]}" stop access-core
move_dogs "$ROOT_ID"
move_dogs "$ANIMALS_ID"
wait_for_prom_value \
    "consumer-group lag for two pending lifecycle events" \
    "seta_asset_event_consumer_group_lag" \
    2
prepare_stale_pending_entries

"${COMPOSE[@]}" up --detach access-core
wait_for_url "Access Core for reclaim and DLQ" "$ACCESS_URL/health"
wait_for_reclaim_and_dlq "$reclaim_baseline_epoch"

echo "Disposable event-backlog recovery scenario completed successfully."
