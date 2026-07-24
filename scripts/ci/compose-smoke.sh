#!/usr/bin/env bash
# Validates the production Docker Compose topology on disposable volumes.
# It intentionally proves only startup, Flyway migrations, and health paths;
# functional integration remains in the service test suites.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
COMPOSE_FILE="$REPO_ROOT/infra/docker-compose.yml"
PROJECT_NAME="${COMPOSE_PROJECT_NAME:-seta-ci-smoke}"
export SETA_COMPOSE_PREFIX="${SETA_COMPOSE_PREFIX:-$PROJECT_NAME}"
export ASSET_DB_PORT="${ASSET_DB_PORT:-15433}"
export ACCESS_DB_PORT="${ACCESS_DB_PORT:-15434}"
export REDIS_PORT="${REDIS_PORT:-16379}"
export GO_ASSET_CORE_PORT="${GO_ASSET_CORE_PORT:-18080}"
export NODE_ACCESS_CORE_PORT="${NODE_ACCESS_CORE_PORT:-14000}"
COMPOSE=(docker compose --project-name "$PROJECT_NAME" --file "$COMPOSE_FILE")

cleanup() {
    status=$?
    if [[ "$status" -ne 0 ]]; then
        echo "Compose smoke failed; collecting diagnostics."
        "${COMPOSE[@]}" ps || true
        "${COMPOSE[@]}" logs --no-color || true
    fi
    "${COMPOSE[@]}" down --volumes --remove-orphans || true
    exit "$status"
}
trap cleanup EXIT

wait_for_health() {
    local name="$1"
    local url="$2"
    local attempts=30

    for ((attempt = 1; attempt <= attempts; attempt++)); do
        if curl --fail --silent "$url" >/dev/null 2>&1; then
            echo "$name is healthy."
            return 0
        fi
        sleep 2
    done

    echo "$name did not become healthy at $url" >&2
    return 1
}

"${COMPOSE[@]}" config --quiet
"${COMPOSE[@]}" build asset-core access-core
"${COMPOSE[@]}" up --detach asset-db access-db redis
"${COMPOSE[@]}" --profile migration run --rm flyway-asset
"${COMPOSE[@]}" --profile migration run --rm flyway-access
"${COMPOSE[@]}" up --detach asset-core access-core

wait_for_health "Asset Core" "http://127.0.0.1:${GO_ASSET_CORE_PORT}/healthz"
wait_for_health "Access Core" "http://127.0.0.1:${NODE_ACCESS_CORE_PORT}/health"

echo "Disposable Compose smoke completed successfully."
