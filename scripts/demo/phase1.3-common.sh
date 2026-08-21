#!/usr/bin/env bash

# Shared runtime and evidence helpers for the interactive Phase 1.3 demo.
# This file is sourced by phase1.3-demo.sh; it does not run a scenario itself.

if [[ "${PHASE13_COMMON_LOADED:-0}" == "1" ]]; then
    return 0
fi
PHASE13_COMMON_LOADED=1

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
COMPOSE_FILE="$REPO_ROOT/infra/docker-compose.yml"
COMPOSE_PREFIX="${SETA_COMPOSE_PREFIX:-seta}"
ACCESS_URL="${PHASE13_ACCESS_URL:-http://127.0.0.1:${NODE_ACCESS_CORE_PORT:-4000}}"
MINIO_URL="${PHASE13_MINIO_URL:-http://127.0.0.1:9000}"
ADMIN_USER="${PHASE13_USER_ID:-00000000-0000-0000-0000-000000000001}"
ORG_ID="${PHASE13_ORG_ID:-00000000-0000-0000-0000-000000000010}"
ASSET_DB_CONTAINER="${COMPOSE_PREFIX}-asset-db"
ACCESS_DB_CONTAINER="${COMPOSE_PREFIX}-access-db"
DELETE_WORKER_CONTAINER="${COMPOSE_PREFIX}-asset-delete-worker"
MEDIA_WORKER_CONTAINER="${COMPOSE_PREFIX}-media-worker"
MINIO_CONTAINER="${COMPOSE_PREFIX}-minio"
ASSET_CORE_CONTAINER="${COMPOSE_PREFIX}-asset-core"
ACCESS_CORE_CONTAINER="${COMPOSE_PREFIX}-access-core"

RUN_ID="${PHASE13_RUN_ID:-phase13-$(date -u +%Y%m%dT%H%M%SZ)-$$}"
if [[ ! "$RUN_ID" =~ ^[A-Za-z0-9._-]+$ ]]; then
    printf 'Invalid PHASE13_RUN_ID: use only letters, numbers, dot, underscore, and hyphen\n' >&2
    return 2 2>/dev/null || exit 2
fi
RUN_STARTED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
LOG_ROOT="${PHASE13_LOG_ROOT:-$REPO_ROOT/demo-logs/$RUN_ID}"
TRANSCRIPT_LOG="$LOG_ROOT/transcript.log"
SUMMARY_FILE="$LOG_ROOT/summary.json"
TEMP_DIR="${TMPDIR:-/tmp}/$RUN_ID"

PHASE13_FAILED=0
PHASE13_FAILURE_MESSAGE=""
PHASE13_CHANGED_MEDIA_WORKER=0
PHASE13_CHANGED_MINIO=0
PHASE13_FOLDER_ID=""
PHASE13_LAST_JSON=""
PHASE13_LAST_STATUS=""
PHASE13_CASE_ID=""
PHASE13_CASE_TITLE=""
PHASE13_CASE_AREA=""
PHASE13_CASE_STARTED_AT=""
PHASE13_CASE_API_START=1
PHASE13_CASE_DB_START=1
PHASE13_CASE_RENDERED=1

phase13_log() { printf '%s\n' "$*"; }
phase13_die() {
    PHASE13_FAILURE_MESSAGE="$*"
    printf 'ERROR: %s\n' "$*" >&2
    return 1
}

phase13_require_command() {
    command -v "$1" >/dev/null 2>&1 || phase13_die "Required command is missing: $1"
}

phase13_uuid() {
    if command -v uuidgen >/dev/null 2>&1; then
        uuidgen | tr '[:upper:]' '[:lower:]'
    else
        tr '[:upper:]' '[:lower:]' </proc/sys/kernel/random/uuid
    fi
}

phase13_pause() {
    local title="$1"
    printf '\n\033[36m=== %s ===\033[0m\n' "$title"
    if [[ "${PHASE13_AUTO_CONTINUE:-0}" != "1" ]]; then
        read -r -p "Press Enter to continue... " _ || true
    fi
    if [[ "$title" =~ ^([0-9]+\.[0-9]+)[[:space:]]+(.+)$ ]]; then
        PHASE13_CASE_ID="${BASH_REMATCH[1]}"
        PHASE13_CASE_TITLE="${BASH_REMATCH[2]}"
        if [[ "$PHASE13_CASE_ID" == 1.* ]]; then
            PHASE13_CASE_AREA="soft-delete"
        else
            PHASE13_CASE_AREA="upload"
        fi
        PHASE13_CASE_API_START=$(( $(wc -l <"$LOG_ROOT/$PHASE13_CASE_AREA/api-responses.jsonl") + 1 ))
        PHASE13_CASE_DB_START=$(( $(wc -l <"$LOG_ROOT/$PHASE13_CASE_AREA/database-snapshots.jsonl") + 1 ))
        PHASE13_CASE_STARTED_AT="$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)"
        PHASE13_CASE_RENDERED=0
    fi
}

phase13_print_case_records() {
    local file="$1" start_line="$2" label="$3" line compact printed=0
    [[ -f "$file" ]] || return 0
    while IFS= read -r line; do
        [[ -n "$line" ]] || continue
        if ! compact="$(jq -cer '.step + ": " + (.payload | tojson)' <<<"$line" 2>/dev/null)"; then
            phase13_die "Refusing to print malformed $label record"
            return 1
        fi
        if (( ${#compact} > 900 )); then
            compact="${compact:0:897}..."
        fi
        if (( printed == 0 )); then
            printf '\033[33m%s\033[0m\n' "$label"
        fi
        printf '  %s\n' "$compact"
        printed=$((printed + 1))
    done < <(tail -n +"$start_line" "$file")
}

phase13_case_service_containers() {
    if [[ "$PHASE13_CASE_AREA" == "soft-delete" ]]; then
        printf '%s\n' "$ACCESS_CORE_CONTAINER" "$ASSET_CORE_CONTAINER" "$DELETE_WORKER_CONTAINER"
    else
        printf '%s\n' "$ACCESS_CORE_CONTAINER" "$ASSET_CORE_CONTAINER" "$MEDIA_WORKER_CONTAINER" "$MINIO_CONTAINER"
    fi
}

phase13_print_case_service_logs() {
    local container raw_log safe_log line printed_header=0
    while IFS= read -r container; do
        [[ -n "$container" ]] || continue
        raw_log="$TEMP_DIR/$PHASE13_CASE_ID-$container.raw.log"
        safe_log="$TEMP_DIR/$PHASE13_CASE_ID-$container.safe.log"
        docker logs --since "$PHASE13_CASE_STARTED_AT" --tail 30 "$container" >"$raw_log" 2>&1 || true
        phase13_sanitize_text <"$raw_log" >"$safe_log"
        if [[ -s "$safe_log" ]]; then
            if (( printed_header == 0 )); then
                printf '\033[33mService logs (new, bounded)\033[0m\n'
                printed_header=1
            fi
            printf '  [%s]\n' "$container"
            while IFS= read -r line; do
                if (( ${#line} > 900 )); then line="${line:0:897}..."; fi
                printf '    %s\n' "$line"
            done < <(tail -n 6 "$safe_log")
            cat "$safe_log" >>"$LOG_ROOT/$PHASE13_CASE_AREA/service-logs/$container.log"
        fi
    done < <(phase13_case_service_containers)
}

phase13_show_case_evidence() {
    local result="${1:-PASS}"
    [[ -n "$PHASE13_CASE_ID" && "$PHASE13_CASE_RENDERED" == "0" ]] || return 0
    PHASE13_CASE_RENDERED=1
    printf '\n\033[35m--- Evidence %s: %s ---\033[0m\n' "$PHASE13_CASE_ID" "$PHASE13_CASE_TITLE"
    phase13_print_case_records "$LOG_ROOT/$PHASE13_CASE_AREA/api-responses.jsonl" \
      "$PHASE13_CASE_API_START" "API evidence"
    phase13_print_case_records "$LOG_ROOT/$PHASE13_CASE_AREA/database-snapshots.jsonl" \
      "$PHASE13_CASE_DB_START" "Database evidence"
    phase13_print_case_service_logs
    if [[ "$result" == "PASS" ]]; then
        printf '\033[32mPASS %s\033[0m\n' "$PHASE13_CASE_ID"
    else
        printf '\033[31mFAIL %s: %s\033[0m\n' "$PHASE13_CASE_ID" "${PHASE13_FAILURE_MESSAGE:-case failed}"
    fi
}

phase13_case_pass() { phase13_show_case_evidence PASS; }
phase13_case_fail() {
    local saved_options="$-"
    set +e
    phase13_show_case_evidence FAIL
    [[ "$saved_options" == *e* ]] && set -e
    return 0
}

phase13_sanitize_json() {
    jq -c '
      def sensitive: test("authorization|token|password|secret|cookie|credential"; "i");
      def clean_url:
        if test("^https?://") and contains("?") then split("?")[0] + "?REDACTED" else . end;
      walk(
        if type == "object" then
          with_entries(if (.key | sensitive) then .value = "REDACTED" else . end)
        elif type == "string" then clean_url
        else . end
      )' <<<"$1"
}

phase13_sanitize_text() {
    sed -E \
      -e 's#(https?://[^[:space:]"?]+)\?[^[:space:]"}]+#\1?REDACTED#g' \
      -e 's/((authorization|token|password|secret|cookie)["=: ]+)[^ ,"}]+/\1REDACTED/Ig'
}

phase13_record_json() {
    local area="$1" kind="$2" step="$3" payload="$4"
    local target="$LOG_ROOT/$area/$kind.jsonl" sanitized
    sanitized="$(phase13_sanitize_json "$payload")" || return 1
    jq -cn --arg at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --arg step "$step" \
        --argjson payload "$sanitized" '{at:$at,step:$step,payload:$payload}' >>"$target"
}

phase13_asset_psql() {
    local sql="$1"
    docker exec "$ASSET_DB_CONTAINER" psql -X -q -v ON_ERROR_STOP=1 -U "${ASSET_DB_USER:-asset_user}" \
        -d "${ASSET_DB_NAME:-asset_db}" -Atc "$sql"
}

phase13_access_psql() {
    local sql="$1"
    docker exec "$ACCESS_DB_CONTAINER" psql -X -q -v ON_ERROR_STOP=1 -U "${ACCESS_DB_USER:-access_user}" \
        -d "${ACCESS_DB_NAME:-access_db}" -Atc "$sql"
}

phase13_record_asset_query() {
    local area="$1" step="$2" sql="$3" rows
    rows="$(phase13_asset_psql "$sql")" || return 1
    phase13_record_json "$area" database-snapshots "$step" \
        "$(jq -cn --arg rows "$rows" '{rows:($rows|split("\n")|map(select(length>0)))}')"
    printf '%s\n' "$rows"
}

phase13_graphql() {
    local area="$1" step="$2" query="$3" variables="$4"
    local request response
    request="$(jq -cn --arg query "$query" --argjson variables "$variables" '{query:$query,variables:$variables}')" || return 1
    response="$(curl -fsS --max-time 30 -X POST "$ACCESS_URL/graphql" \
        -H 'Content-Type: application/json' -H "x-user-id: $ADMIN_USER" -H "x-org-id: $ORG_ID" \
        --data "$request")" || return 1
    jq -e . >/dev/null <<<"$response" || phase13_die "$step returned invalid JSON"
    phase13_record_json "$area" api-responses "$step" "$response"
    if jq -e '.errors and (.errors | length > 0)' >/dev/null <<<"$response"; then
        phase13_die "$step failed: $(jq -r '.errors[0].extensions.code // "UNKNOWN"' <<<"$response")"
    fi
    PHASE13_LAST_JSON="$response"
}

phase13_http_json() {
    local area="$1" step="$2" method="$3" url="$4" expected="$5" body="${6:-}"
    local output="$TEMP_DIR/http-body.json" status
    local args=(-sS --max-time 65 -o "$output" -w '%{http_code}' -X "$method" "$url"
        -H 'Content-Type: application/json' -H "x-user-id: $ADMIN_USER" -H "x-org-id: $ORG_ID")
    [[ -n "$body" ]] && args+=(--data "$body")
    status="$(curl "${args[@]}")" || return 1
    PHASE13_LAST_JSON="$(<"$output")"
    PHASE13_LAST_STATUS="$status"
    jq -e . >/dev/null <<<"$PHASE13_LAST_JSON" || phase13_die "$step returned invalid JSON (HTTP $status)"
    phase13_record_json "$area" api-responses "$step" \
        "$(jq -cn --arg status "$status" --argjson body "$PHASE13_LAST_JSON" '{httpStatus:($status|tonumber),body:$body}')"
    [[ "$status" == "$expected" ]] || phase13_die "$step returned HTTP $status, expected $expected"
}

phase13_create_upload_session() {
    local step="$1" asset_id="$2" file="$3" filename="$4"
    local size checksum key body output="$TEMP_DIR/http-body.json" status
    size="$(wc -c <"$file" | tr -d ' ')"
    checksum="$(openssl dgst -sha256 -binary "$file" | base64 | tr -d '\n')"
    key="$(phase13_uuid)"
    body="$(jq -cn --arg filename "$filename" --arg checksum "$checksum" --argjson size "$size" \
        '{filename:$filename,contentType:"image/png",sizeBytes:$size,checksumSha256:$checksum}')"
    status="$(curl -sS --max-time 65 -o "$output" -w '%{http_code}' -X POST \
        "$ACCESS_URL/api/v1/assets/$asset_id/media/uploads" -H 'Content-Type: application/json' \
        -H "x-user-id: $ADMIN_USER" -H "x-org-id: $ORG_ID" -H "Idempotency-Key: $key" --data "$body")" || return 1
    PHASE13_LAST_JSON="$(<"$output")"
    PHASE13_LAST_STATUS="$status"
    jq -e . >/dev/null <<<"$PHASE13_LAST_JSON" || phase13_die "$step returned invalid JSON"
    phase13_record_json upload api-responses "$step" \
        "$(jq -cn --arg status "$status" --argjson body "$PHASE13_LAST_JSON" '{httpStatus:($status|tonumber),body:$body}')"
    [[ "$status" == "201" ]] || phase13_die "$step returned HTTP $status, expected 201"
}

phase13_transfer_upload() {
    local step="$1" descriptor_json="$2" file="$3"
    local url method status
    url="$(jq -r '.data.upload.url' <<<"$descriptor_json")"
    method="$(jq -r '.data.upload.method' <<<"$descriptor_json")"
    [[ "$url" == http://* || "$url" == https://* ]] || phase13_die "$step did not receive a signed URL"
    local args=(-sS --max-time 65 -o /dev/null -w '%{http_code}' -X "$method" "$url")
    while IFS=$'\t' read -r name value; do
        [[ -n "$name" ]] && args+=(-H "$name: $value")
    done < <(jq -r '.data.upload.headers | to_entries[] | [.key,.value] | @tsv' <<<"$descriptor_json")
    args+=(--data-binary "@$file")
    status="$(curl "${args[@]}")" || return 1
    phase13_record_json upload api-responses "$step" \
        "$(jq -cn --arg status "$status" --arg method "$method" --arg url "$url" \
          '{httpStatus:($status|tonumber),method:$method,url:$url,body:"OMITTED_BINARY"}')"
    [[ "$status" =~ ^2 ]] || phase13_die "$step returned HTTP $status"
}

phase13_wait_asset_sql() {
    local description="$1" sql="$2" expected="$3" timeout="${4:-90}" value deadline
    deadline=$((SECONDS + timeout))
    while (( SECONDS < deadline )); do
        value="$(phase13_asset_psql "$sql")" || return 1
        if [[ "$value" == "$expected" ]]; then
            printf '%s\n' "$value"
            return 0
        fi
        sleep 0.25
    done
    phase13_die "Timed out waiting for $description; last value: ${value:-<empty>}"
}

phase13_container_running() {
    [[ "$(docker inspect -f '{{.State.Running}}' "$1" 2>/dev/null)" == "true" ]]
}

phase13_wait_container_running() {
    local container="$1" deadline=$((SECONDS + 45))
    while (( SECONDS < deadline )); do
        phase13_container_running "$container" && return 0
        sleep 0.5
    done
    phase13_die "Container did not become running: $container"
}

phase13_stop_media_worker() {
    if phase13_container_running "$MEDIA_WORKER_CONTAINER"; then
        docker stop -t 2 "$MEDIA_WORKER_CONTAINER" >/dev/null
        PHASE13_CHANGED_MEDIA_WORKER=1
    fi
}

phase13_start_media_worker() {
    docker start "$MEDIA_WORKER_CONTAINER" >/dev/null
    PHASE13_CHANGED_MEDIA_WORKER=1
    phase13_wait_container_running "$MEDIA_WORKER_CONTAINER"
}

phase13_stop_minio() {
    if phase13_container_running "$MINIO_CONTAINER"; then
        docker stop -t 2 "$MINIO_CONTAINER" >/dev/null
        PHASE13_CHANGED_MINIO=1
    fi
}

phase13_start_minio() {
    docker start "$MINIO_CONTAINER" >/dev/null
    PHASE13_CHANGED_MINIO=1
    phase13_wait_container_running "$MINIO_CONTAINER"
    local deadline=$((SECONDS + 45))
    until curl -fsS --max-time 2 "$MINIO_URL/minio/health/live" >/dev/null; do
        (( SECONDS < deadline )) || phase13_die "MinIO did not become ready"
        sleep 0.5
    done
}

phase13_capture_service_logs() {
    local area="$1"; shift
    local container raw_log
    for container in "$@"; do
        raw_log="$TEMP_DIR/$container.raw.log"
        docker logs --since "$RUN_STARTED_AT" --tail 500 "$container" \
            >"$raw_log" 2>&1 || true
        phase13_sanitize_text <"$raw_log" >"$LOG_ROOT/$area/service-logs/$container.log"
    done
}

phase13_create_demo_folder() {
    local area="$1"
    [[ -n "$PHASE13_FOLDER_ID" ]] && return 0
    phase13_graphql "$area" create-folder \
      'mutation($orgId:ID!,$name:String!){createFolder(orgId:$orgId,name:$name){id name path}}' \
      "$(jq -cn --arg orgId "$ORG_ID" --arg name "$RUN_ID" '{orgId:$orgId,name:$name}')"
    PHASE13_FOLDER_ID="$(jq -r '.data.createFolder.id' <<<"$PHASE13_LAST_JSON")"
    [[ "$PHASE13_FOLDER_ID" =~ ^[0-9a-f-]{36}$ ]] || phase13_die "Folder creation returned an invalid ID"
}

phase13_create_metadata() {
    local area="$1" title="$2"
    phase13_graphql "$area" create-metadata \
      'mutation($orgId:ID!,$input:CreateMetadataInput!){createMetadata(orgId:$orgId,input:$input){id folderId title}}' \
      "$(jq -cn --arg orgId "$ORG_ID" --arg folderId "$PHASE13_FOLDER_ID" --arg title "$title" \
        '{orgId:$orgId,input:{folderId:$folderId,title:$title,labels:["phase1.3-demo"],metadataJson:"{}"}}')"
    jq -r '.data.createMetadata.id' <<<"$PHASE13_LAST_JSON"
}

phase13_write_summary() {
    local status="$1" message="${2:-}" finished
    finished="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    jq -n --arg runId "$RUN_ID" --arg status "$status" --arg startedAt "$RUN_STARTED_AT" \
      --arg finishedAt "$finished" --arg message "$message" --arg logRoot "$LOG_ROOT" \
      '{runId:$runId,status:$status,startedAt:$startedAt,finishedAt:$finishedAt,message:$message,evidenceDirectory:$logRoot}' \
      >"$SUMMARY_FILE"
}

phase13_restore_services() {
    local failed=0
    if [[ "$PHASE13_CHANGED_MINIO" == "1" ]] && ! phase13_container_running "$MINIO_CONTAINER"; then
        docker start "$MINIO_CONTAINER" >/dev/null || failed=1
    fi
    if [[ "$PHASE13_CHANGED_MEDIA_WORKER" == "1" ]] && ! phase13_container_running "$MEDIA_WORKER_CONTAINER"; then
        docker start "$MEDIA_WORKER_CONTAINER" >/dev/null || failed=1
    fi
    return "$failed"
}

phase13_preflight() {
    local command container
    for command in bash curl docker jq openssl base64 wc; do phase13_require_command "$command"; done
    [[ "${BASH_VERSINFO[0]}" -ge 4 ]] || phase13_die "Bash 4 or newer is required"
    for container in "$ASSET_DB_CONTAINER" "$ACCESS_DB_CONTAINER" "$DELETE_WORKER_CONTAINER" \
      "$MEDIA_WORKER_CONTAINER" "$MINIO_CONTAINER" "$ASSET_CORE_CONTAINER" "$ACCESS_CORE_CONTAINER"; do
        phase13_container_running "$container" || phase13_die "Required existing container is not running: $container"
    done
    curl -fsS --max-time 3 "$ACCESS_URL/health" >/dev/null || phase13_die "Access Core is not healthy at $ACCESS_URL"
    phase13_access_psql "SELECT 1 FROM access.organization_members WHERE org_id='$ORG_ID'::uuid AND user_id='$ADMIN_USER'::uuid LIMIT 1;" \
      | grep -qx 1 || phase13_die "Demo admin membership is missing; apply infra/db/access/seed/demo_fixtures.sql"
    phase13_asset_psql "SELECT 1 FROM flyway_schema_history WHERE success AND version='12' LIMIT 1;" \
      | grep -qx 1 || phase13_die "Asset database is not migrated through V12"
}

phase13_initialize_evidence() {
    mkdir -p "$LOG_ROOT/soft-delete/service-logs" "$LOG_ROOT/upload/service-logs" "$TEMP_DIR"
    : >"$LOG_ROOT/soft-delete/api-responses.jsonl"
    : >"$LOG_ROOT/soft-delete/database-snapshots.jsonl"
    : >"$LOG_ROOT/upload/api-responses.jsonl"
    : >"$LOG_ROOT/upload/database-snapshots.jsonl"
    : >"$TRANSCRIPT_LOG"
    phase13_write_summary RUNNING ""
}

phase13_finish() {
    local exit_code="$1"
    phase13_restore_services || true
    if (( exit_code == 0 )); then
        phase13_write_summary SUCCEEDED "All requested scenarios completed"
    else
        phase13_write_summary FAILED "${PHASE13_FAILURE_MESSAGE:-Demo stopped with exit code $exit_code}"
    fi
    rm -rf "$TEMP_DIR"
}
