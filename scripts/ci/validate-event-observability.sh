#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
PROMETHEUS_DIR="$REPO_ROOT/infra/observability/prometheus"
DASHBOARD="$REPO_ROOT/infra/observability/grafana/dashboards/seta-event-model.json"
COMPOSE_FILES=(
    -f "$REPO_ROOT/infra/docker-compose.yml"
    -f "$REPO_ROOT/infra/docker-compose.observability.yml"
)

jq --exit-status 'type == "object"' "$DASHBOARD" >/dev/null
docker compose "${COMPOSE_FILES[@]}" --profile observability config --quiet
docker run --rm \
    --entrypoint /bin/promtool \
    --volume "$PROMETHEUS_DIR:/etc/prometheus:ro" \
    prom/prometheus:v2.54.1 \
    check config /etc/prometheus/prometheus.yml

echo "Event-model dashboard, Compose topology, Prometheus config, and recording rules are valid."
