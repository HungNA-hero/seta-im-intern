#!/usr/bin/env bash
# Exercises event-model metrics against a disposable, uniquely named Compose project.
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
PROJECT_NAME="${COMPOSE_PROJECT_NAME:-seta-event-model-$$}"
export SETA_COMPOSE_PREFIX="${SETA_COMPOSE_PREFIX:-$PROJECT_NAME}"
export ASSET_DB_PORT="${ASSET_DB_PORT:-25433}"
export ACCESS_DB_PORT="${ACCESS_DB_PORT:-25434}"
export REDIS_PORT="${REDIS_PORT:-26379}"
export GO_ASSET_CORE_PORT="${GO_ASSET_CORE_PORT:-28080}"
export NODE_ACCESS_CORE_PORT="${NODE_ACCESS_CORE_PORT:-24000}"
export PROMETHEUS_PORT="${PROMETHEUS_PORT:-29090}"
export REDIS_EXPORTER_PORT="${REDIS_EXPORTER_PORT:-29121}"
export GRAFANA_PORT="${GRAFANA_PORT:-23000}"
export LOKI_PORT="${LOKI_PORT:-23100}"
export ALLOY_PORT="${ALLOY_PORT:-22345}"

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
PROMETHEUS_URL="http://127.0.0.1:${PROMETHEUS_PORT}"

cleanup() {
    status=$?
    if [[ "$status" -ne 0 ]]; then
        echo "Event-model scenario failed; collecting diagnostics." >&2
        "${COMPOSE[@]}" ps || true
        "${COMPOSE[@]}" logs --no-color --tail 200 \
            asset-core asset-delete-worker access-core redis redis-exporter prometheus || true
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

wait_for_prom_absent() {
    local description="$1"
    local query="$2"
    local response
    for _ in {1..45}; do
        response="$(prom_query "$query")"
        if jq --exit-status \
            '.status == "success" and (.data.result | length) == 0' \
            >/dev/null <<<"$response"; then
            echo "$description observed."
            return 0
        fi
        sleep 2
    done
    echo "Timed out waiting for $description; last response: $response" >&2
    return 1
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

verify_bounded_series_labels() {
    local response label_keys forbidden normalized_labels
    response="$(
        curl --fail --silent --get "$PROMETHEUS_URL/api/v1/series" \
            --data-urlencode \
            'match[]={__name__=~"seta_asset_lifecycle_event_publish_total|seta_asset_event_.*|redis_stream_.*"}'
    )"
    jq --exit-status '.status == "success" and (.data | length) > 0' >/dev/null <<<"$response"
    label_keys="$(jq -r '.data[] | keys[]' <<<"$response" | sort -u)"
    forbidden="$(
        grep -Ei 'org|user|resource|trace|payload|event|message|consumer' \
            <<<"$label_keys" || true
    )"
    if [[ -n "$forbidden" ]]; then
        echo "Identifier-bearing metric labels found: $forbidden" >&2
        return 1
    fi
    normalized_labels="$(
        jq -r '
          .data[]
          | select(.__name__ | startswith("seta_asset_event_"))
          | del(.__name__)
          | keys[]
        ' <<<"$response" | sort -u
    )"
    if [[ -n "$normalized_labels" ]]; then
        echo "Normalized event gauges unexpectedly retain labels: $normalized_labels" >&2
        return 1
    fi

    wait_for_prom_value \
        "absence of per-consumer Redis Stream series" \
        'absent({__name__=~"redis_stream_group_consumer_.*"})' \
        1
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
"${COMPOSE[@]}" up --detach asset-core asset-delete-worker access-core prometheus

wait_for_url "Asset Core" "$ASSET_URL/healthz"
wait_for_url "Prometheus" "$PROMETHEUS_URL/-/ready"
wait_for_prom_value \
    "healthy independent Redis observer" \
    "seta_asset_event_stream_observer_up" \
    1
wait_for_prom_value \
    "cache-invalidator consumer group" \
    "seta_asset_event_consumer_group_present" \
    1
wait_for_prom_value \
    "asset-delete-worker metrics scrape" \
    'up{job="asset-delete-worker"}' \
    1

move_dogs "$ROOT_ID"
wait_for_prom_value \
    "successful Asset Core publication" \
    'seta_asset_lifecycle_event_publish_total{service="asset-core",outcome="success"}' \
    1

"${COMPOSE[@]}" stop access-core
move_dogs "$ANIMALS_ID"
wait_for_prom_value \
    "consumer-group lag while Access Core is stopped" \
    "seta_asset_event_consumer_group_lag" \
    1

"${COMPOSE[@]}" exec --no-TTY redis redis-cli \
    XREADGROUP GROUP cache-invalidator scenario-pending COUNT 1 \
    STREAMS stream:asset-events ">" >/dev/null
wait_for_prom_value \
    "delivered but unacknowledged pending work" \
    "seta_asset_event_consumer_group_pending" \
    1

"${COMPOSE[@]}" exec --no-TTY redis redis-cli \
    XADD stream:asset-events:dlq "*" payload scenario-probe >/dev/null
wait_for_prom_value \
    "synthetic DLQ depth" \
    "seta_asset_event_dlq_depth" \
    1

verify_bounded_series_labels

"${COMPOSE[@]}" stop redis
move_dogs "$ROOT_ID"
wait_for_prom_value \
    "failed post-commit publication" \
    'seta_asset_lifecycle_event_publish_total{service="asset-core",outcome="failure"}' \
    1
wait_for_prom_value \
    "observer-down state after Redis stops" \
    "1 - seta_asset_event_stream_observer_up" \
    1
wait_for_prom_absent \
    "unavailable lag gauge while observer is down" \
    "seta_asset_event_consumer_group_lag"

echo "Disposable event-model scenario completed successfully."
