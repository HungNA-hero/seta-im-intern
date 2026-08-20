#!/usr/bin/env bash
#
# Phase 1.3 release-demo runner.
#
# This runner is intentionally separate from trainer-demo.sh and Sprint 4:
# it exercises only the Phase 1.3 release flows. Scenario implementations are
# added incrementally; `--list` is always safe and needs no running services.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
COMPOSE=(docker compose -f "$REPO_ROOT/infra/docker-compose.yml")

ACCESS_URL="${PHASE13_ACCESS_URL:-http://127.0.0.1:4000}"
ADMIN_USER="${PHASE13_ADMIN_USER:-00000000-0000-0000-0000-000000000001}"
ORG_ID="${PHASE13_ORG_ID:-00000000-0000-0000-0000-000000000010}"
ROOT_FOLDER_ID="${PHASE13_ROOT_FOLDER_ID:-10000000-0000-0000-0000-000000000000}"
MEDIA_FIXTURE="${PHASE13_MEDIA_FIXTURE:-$REPO_ROOT/services/asset-core/testdata/media/valid/landscape-2048x1152.jpg}"
MEDIA_BUCKET="${ASSET_MEDIA_BUCKET:-seta-media}"
RETENTION_TIMEZONE="${ASSET_RETENTION_CLEANUP_TIMEZONE:-Asia/Bangkok}"
READINESS_TIMEOUT_SECONDS="${PHASE13_READINESS_TIMEOUT_SECONDS:-90}"
JOB_TIMEOUT_SECONDS="${PHASE13_JOB_TIMEOUT_SECONDS:-120}"

INTERACTIVE=0
NON_INTERACTIVE=0
LIST_ONLY=0
SETUP=0
SCENARIOS=()
RUN_ID="p13-$(date -u +%Y%m%dT%H%M%SZ)-$$"
HTTP_STATUS=""
HTTP_BODY=""
HTTP_BODY_FILE=""

usage() {
    cat <<'EOF'
Usage:
  ./scripts/phase1.3/run-demo.sh --list
  ./scripts/phase1.3/run-demo.sh --setup
  ./scripts/phase1.3/run-demo.sh (--interactive|--non-interactive) --scenario M1|L1|L2|R1

Options:
  --setup                 Start the local release stack, run Flyway, and apply
                          the idempotent demo fixtures. It does not reset volumes.
  --interactive           Pause between SAY / ACT / SHOW stages for a live demo.
  --non-interactive       Run a rehearsal without pauses.
  --scenario ID           Run one implemented scenario. This slice implements M1, L1, L2, and R1.
  --list                  Print the complete release-demo catalogue.

Environment overrides:
  PHASE13_ACCESS_URL, PHASE13_ADMIN_USER, PHASE13_ORG_ID,
  PHASE13_ROOT_FOLDER_ID, PHASE13_MEDIA_FIXTURE,
  PHASE13_READINESS_TIMEOUT_SECONDS, PHASE13_JOB_TIMEOUT_SECONDS.
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --interactive) INTERACTIVE=1; shift ;;
        --non-interactive) NON_INTERACTIVE=1; shift ;;
        --setup) SETUP=1; shift ;;
        --list) LIST_ONLY=1; shift ;;
        --scenario)
            [[ $# -ge 2 ]] || { echo "--scenario requires an ID." >&2; exit 2; }
            SCENARIOS+=("$2"); shift 2 ;;
        --help|-h) usage; exit 0 ;;
        *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
    esac
done

scenario_catalog() {
    cat <<'EOF'
Main demo
  M1  Upload, durable event delivery, and rendition promotion
  L1  Folder and metadata soft delete; root-only Recycle Bin
  L2  Parent-first asynchronous restore with a nested deletion root
  R1  Daily retention cleanup and physical media purge

Evidence appendix
  E1  Authorization boundary: read can list; write is required for delete/restore
  E2  Restore collision: a restored name becomes "(1)" instead of overwriting
  E3  Safe cursor contract: malformed public cursor returns CURSOR_INVALID
  E4  Scheduler idempotency and nested-root retention deferral
  E5  Lease renewal and retry safety, explained with focused test evidence
  E6  Upload idempotency and duplicate-event convergence
EOF
}

log() { printf '%s\n' "$*"; }
die() { printf '[FAIL] %s\n' "$*" >&2; exit 1; }
pass() { printf '[PASS] %s\n' "$*"; }

pause() {
    if [[ "$INTERACTIVE" -eq 1 ]]; then
        read -r -p "Press Enter to continue..." _ || true
    fi
}

say() {
    printf '\n\033[36mSAY\033[0m  %s\n' "$1"
    pause
}

act() { printf '\033[33mACT\033[0m  %s\n' "$1"; }
show() { printf '\033[35mSHOW\033[0m %s\n' "$1"; }
assertion() { printf '\033[32mASSERT\033[0m %s\n' "$1"; }

require_command() {
    command -v "$1" >/dev/null 2>&1 || die "$1 is required. Run this package from Ubuntu/WSL2 or Linux."
}

require_tools() {
    require_command curl
    require_command jq
    require_command openssl
    require_command docker
}

assert_equals() {
    local expected="$1" actual="$2" label="$3"
    [[ "$expected" == "$actual" ]] || die "$label: expected '$expected', got '$actual'."
    pass "$label"
}

assert_true() {
    local actual="$1" label="$2"
    [[ "$actual" == "true" ]] || die "$label: expected true, got '$actual'."
    pass "$label"
}

cleanup_http_body() {
    if [[ -n "$HTTP_BODY_FILE" && -f "$HTTP_BODY_FILE" ]]; then
        rm -f "$HTTP_BODY_FILE"
    fi
    return 0
}
trap cleanup_http_body EXIT

# request_json METHOD URL JSON_BODY [HEADER...]
# The body and status are captured separately: an application error is useful
# demo evidence, so curl must not hide it behind a generic transport failure.
request_json() {
    local method="$1" url="$2" body="$3"
    shift 3
    cleanup_http_body
    HTTP_BODY_FILE="$(mktemp)"
    local -a args=(-sS --connect-timeout 10 --max-time 90 -o "$HTTP_BODY_FILE" -w '%{http_code}' -X "$method" "$url")
    local header
    for header in "$@"; do args+=(-H "$header"); done
    [[ -n "$body" ]] && args+=(--data "$body")
    HTTP_STATUS="$(curl "${args[@]}")" || die "HTTP transport failed for $method $url."
    HTTP_BODY="$(<"$HTTP_BODY_FILE")"
}

print_json() {
    local title="$1" json="$2" filter="${3:-.}"
    show "$title"
    jq "$filter" <<<"$json"
}

# graphql_data QUERY VARIABLES_JSON -> prints .data only and rejects GraphQL errors.
graphql_data() {
    local query="$1" variables="$2" body
    body="$(jq -nc --arg q "$query" --argjson v "$variables" '{query:$q, variables:$v}')"
    request_json POST "$ACCESS_URL/graphql" "$body" \
        'content-type: application/json' "x-user-id: $ADMIN_USER" "x-org-id: $ORG_ID"
    [[ "$HTTP_STATUS" == "200" ]] || die "GraphQL HTTP response: expected '200', got '$HTTP_STATUS'."
    # This helper is often called inside command substitution, so only this
    # progress line uses stderr; stdout remains the JSON data for the caller.
    printf '[PASS] GraphQL HTTP response\n' >&2
    if ! jq -e '(.errors // []) | length == 0' >/dev/null <<<"$HTTP_BODY"; then
        jq . <<<"$HTTP_BODY" >&2
        die "GraphQL returned an application error."
    fi
    jq -c '.data' <<<"$HTTP_BODY"
}

asset_db_json() {
    local sql="$1"
    "${COMPOSE[@]}" exec -T asset-db psql -U asset_user -d asset_db -Atqc "$sql" | tr -d '\r'
}

wait_http_ok() {
    local url="$1" label="$2" deadline=$((SECONDS + READINESS_TIMEOUT_SECONDS))
    while (( SECONDS < deadline )); do
        if curl -fsS --max-time 2 "$url" >/dev/null 2>&1; then
            pass "$label"
            return 0
        fi
        sleep 1
    done
    die "$label did not become ready within ${READINESS_TIMEOUT_SECONDS}s."
}

apply_seed() {
    local service="$1" database="$2" file="$3" user="$4"
    "${COMPOSE[@]}" exec -T "$service" psql -v ON_ERROR_STOP=1 -U "$user" -d "$database" < "$file"
}

setup_release_stack() {
    require_tools
    act "Starting Phase 1.3 dependencies and applying Flyway migrations."
    "${COMPOSE[@]}" up -d asset-db access-db redis minio kafka
    "${COMPOSE[@]}" --profile migration run --rm flyway-asset
    "${COMPOSE[@]}" --profile migration run --rm flyway-access

    act "Applying the idempotent local demo organization, users, and root folder."
    apply_seed access-db access_db "$REPO_ROOT/infra/db/access/seed/demo_fixtures.sql" access_user
    apply_seed asset-db asset_db "$REPO_ROOT/infra/db/asset/seed/demo_fixtures.sql" asset_user

    # Topic and bucket creation finish before workers use them. Upload and the
    # localhost HTTP exception are scoped to this development-only Compose
    # invocation; production defaults remain off and HTTPS-required in the
    # checked-in Compose configuration.
    "${COMPOSE[@]}" up -d minio-init kafka-init
    NODE_ENV=development ACCESS_MEDIA_UPLOAD_ENABLED=true ACCESS_MEDIA_REQUIRE_HTTPS=false "${COMPOSE[@]}" up -d --build \
        asset-core asset-delete-worker media-worker access-core
    wait_http_ok "$ACCESS_URL/health" "Access Core health"
    wait_http_ok "http://127.0.0.1:8080/healthz" "Asset Core health"
    log "Setup is complete. No volume was removed and no existing row was reset."
}

create_metadata_asset() {
    local title="$1" data
    data="$(graphql_data \
        'mutation($orgId: ID!, $input: CreateMetadataInput!) { createMetadata(orgId: $orgId, input: $input) { id title folderId } }' \
        "$(jq -nc --arg org "$ORG_ID" --arg folder "$ROOT_FOLDER_ID" --arg title "$title" '{orgId:$org, input:{folderId:$folder, title:$title, metadataJson:"{\"phase\":\"1.3\",\"demo\":true}"}}')")"
    jq -er '.createMetadata.id' <<<"$data"
}

create_metadata_in_folder() {
    local folder_id="$1" title="$2" metadata_json data
    metadata_json="${3:-}"
    if [[ -z "$metadata_json" ]]; then
        metadata_json='{"phase":"1.3","scenario":"L1"}'
    fi
    data="$(graphql_data \
        'mutation($orgId: ID!, $input: CreateMetadataInput!) { createMetadata(orgId: $orgId, input: $input) { id title folderId } }' \
        "$(jq -nc --arg org "$ORG_ID" --arg folder "$folder_id" --arg title "$title" --arg metadataJson "$metadata_json" '{orgId:$org, input:{folderId:$folder, title:$title, metadataJson:$metadataJson}}')")"
    printf '%s' "$data"
}

create_folder() {
    local name="$1" parent_path="$2" data
    data="$(graphql_data \
        'mutation($orgId: ID!, $parentPath: String!, $name: String!) { createFolder(orgId: $orgId, parentPath: $parentPath, name: $name) { id name path } }' \
        "$(jq -nc --arg org "$ORG_ID" --arg parentPath "$parent_path" --arg name "$name" '{orgId:$org, parentPath:$parentPath, name:$name}')")"
    printf '%s' "$data"
}

upload_binary_to_session() {
    local session_json="$1" file="$2" method url header status
    method="$(jq -er '.data.upload.method' <<<"$session_json")"
    url="$(jq -er '.data.upload.url' <<<"$session_json")"
    local -a args=(-sS --connect-timeout 10 --max-time 90 -o /dev/null -w '%{http_code}' -X "$method" "$url")
    while IFS= read -r header; do args+=(-H "$header"); done < <(jq -r '.data.upload.headers | to_entries[] | "\(.key): \(.value)"' <<<"$session_json")
    args+=(--data-binary "@$file")
    status="$(curl "${args[@]}")" || die "Direct upload to private object storage failed."
    [[ "$status" =~ ^20[0-9]$ ]] || die "Direct upload returned HTTP $status, expected a 2xx response."
    pass "Binary was accepted by the presigned private-object upload URL"
}

wait_for_media_completion() {
    local job_id="$1" deadline=$((SECONDS + JOB_TIMEOUT_SECONDS)) row status
    while (( SECONDS < deadline )); do
        row="$(asset_db_json "
            SELECT json_build_object(
              'jobStatus', job.status,
              'outboxStatus', outbox.status,
              'outboxPublishedAt', outbox.published_at,
              'versionStatus', version.status,
              'activeVersionId', asset.active_media_version_id,
              'outputCount', count(output.id)
            )
            FROM media_processing_jobs AS job
            JOIN asset_media_versions AS version ON version.id = job.version_id
            JOIN metadata_items AS asset ON asset.id = job.asset_id
            LEFT JOIN media_job_outbox AS outbox ON outbox.job_id = job.id
            LEFT JOIN media_outputs AS output ON output.version_id = version.id
            WHERE job.id = '$job_id'
            GROUP BY job.status, outbox.status, outbox.published_at, version.status, asset.active_media_version_id;")"
        [[ -n "$row" ]] || die "Media job $job_id was not found in Asset DB."
        status="$(jq -r '.jobStatus' <<<"$row")"
        if [[ "$status" == "completed" ]]; then
            printf '%s' "$row"
            return 0
        fi
        if [[ "$status" == "failed" ]]; then
            jq . <<<"$row" >&2
            die "Media worker reached terminal failed state."
        fi
        sleep 1
    done
    die "Media job $job_id did not finish within ${JOB_TIMEOUT_SECONDS}s."
}

minio_stat() {
    local object_key="$1"
    "${COMPOSE[@]}" run --rm --no-deps --entrypoint /bin/sh minio-init -c \
        'mc alias set local "$MINIO_INTERNAL_ENDPOINT" "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null && mc stat --json "local/$MEDIA_BUCKET/$1"' \
        demo "$object_key"
}

assert_minio_object_absent() {
    local object_key="$1"
    if minio_stat "$object_key" >/dev/null 2>&1; then
        die "MinIO object $object_key still exists after physical purge."
    fi
    pass "MinIO object is absent after physical purge: $object_key"
}

# expire_retention_fixture is the sole state-setting shortcut in R1. It changes
# only the retention clock of an isolated unit that was created and deleted by
# the public API and real workers. It never creates a cleanup run or PURGE job.
expire_retention_fixture() {
    local unit_id="$1" retention_until
    retention_until="$("${COMPOSE[@]}" exec -T asset-db psql -v ON_ERROR_STOP=1 -U asset_user -d asset_db -Atqc "
        UPDATE asset_lifecycle_units
        SET retention_until = statement_timestamp() - interval '1 second'
        WHERE id = '$unit_id'
          AND org_id = '$ORG_ID'
          AND state = 'DELETED'
        RETURNING retention_until;
    " | tr -d '\r')"
    [[ -n "$retention_until" ]] || die "R1 fixture lifecycle unit was not in DELETED state to expire its retention clock."
    printf '%s' "$retention_until"
}

wait_for_cleanup_run() {
    local deadline=$((SECONDS + JOB_TIMEOUT_SECONDS)) row
    while (( SECONDS < deadline )); do
        row="$(asset_db_json "
            SELECT json_build_object(
                'runId', id,
                'runDate', run_date,
                'timezone', timezone,
                'status', status,
                'checkpoint', checkpoint,
                'createdAt', created_at,
                'completedAt', completed_at
            )
            FROM asset_lifecycle_cleanup_runs
            WHERE scheduler_name = 'daily-retention-cleanup'
              AND run_date = (statement_timestamp() AT TIME ZONE '$RETENTION_TIMEZONE')::date
            ORDER BY created_at DESC
            LIMIT 1;")"
        if [[ -n "$row" ]]; then
            printf '%s' "$row"
            return 0
        fi
        sleep 1
    done
    die "Daily cleanup run was not created within ${JOB_TIMEOUT_SECONDS}s."
}

# A real daily cleanup run is deliberately unique per scheduler and local
# calendar day. Check before R1 creates a fixture so a second rehearsal does
# not leave data that cannot be selected again until tomorrow.
assert_r1_daily_slot_available() {
    local existing_run
    existing_run="$(asset_db_json "
        SELECT json_build_object('id', id, 'status', status, 'runDate', run_date)
        FROM asset_lifecycle_cleanup_runs
        WHERE scheduler_name = 'daily-retention-cleanup'
          AND run_date = (statement_timestamp() AT TIME ZONE '$RETENTION_TIMEZONE')::date
        ORDER BY created_at DESC
        LIMIT 1;")"
    if [[ -n "$existing_run" ]]; then
        print_json "Existing daily cleanup run" "$existing_run"
        die "R1 has already consumed today's real cleanup slot. Use a fresh disposable database or run R1 tomorrow; do not delete the run record to fake scheduler evidence."
    fi
}

wait_for_purge_job() {
    local unit_id="$1" deadline=$((SECONDS + JOB_TIMEOUT_SECONDS)) row
    while (( SECONDS < deadline )); do
        row="$(asset_db_json "
            SELECT json_build_object(
                'jobId', job.id,
                'status', job.status,
                'attempts', job.attempts,
                'operation', job.operation,
                'queuedAt', job.queued_at,
                'unitState', unit.state
            )
            FROM asset_lifecycle_jobs AS job
            JOIN asset_lifecycle_units AS unit ON unit.id = job.unit_id
            WHERE job.unit_id = '$unit_id'
              AND job.operation = 'PURGE'
            ORDER BY job.queued_at DESC
            LIMIT 1;")"
        if [[ -n "$row" ]]; then
            printf '%s' "$row"
            return 0
        fi
        sleep 1
    done
    die "PURGE job for lifecycle unit $unit_id was not queued within ${JOB_TIMEOUT_SECONDS}s."
}

wait_for_purge_completion() {
    local unit_id="$1" deadline=$((SECONDS + JOB_TIMEOUT_SECONDS)) row status
    while (( SECONDS < deadline )); do
        row="$(wait_for_purge_job "$unit_id")"
        status="$(jq -r '.status' <<<"$row")"
        case "$status" in
            SUCCEEDED)
                printf '%s' "$row"
                return 0
                ;;
            FAILED|SUPPRESSED)
                jq . <<<"$row" >&2
                die "PURGE job reached terminal $status state."
                ;;
        esac
        sleep 1
    done
    die "PURGE job for lifecycle unit $unit_id did not finish within ${JOB_TIMEOUT_SECONDS}s."
}

run_m1() {
    require_tools
    [[ -f "$MEDIA_FIXTURE" ]] || die "M1 image fixture does not exist: $MEDIA_FIXTURE"
    wait_http_ok "$ACCESS_URL/health" "Access Core health before M1"

    local title asset_id size checksum session_body upload_id commit_body job_id completion evidence raw_key output_key
    local -a output_keys
    title="$RUN_ID-media"

    say "The terminal is the client. I first create one isolated metadata asset through GraphQL, then request an authorized upload session for it."
    act "Creating one M1 metadata asset through the public GraphQL API."
    asset_id="$(create_metadata_asset "$title")"
    assertion "The asset exists because GraphQL returned its server-generated ID: $asset_id"

    size="$(wc -c < "$MEDIA_FIXTURE" | tr -d '[:space:]')"
    checksum="$(openssl dgst -sha256 -binary "$MEDIA_FIXTURE" | base64 | tr -d '\n')"
    act "Requesting a checksum-bound upload session; the API receives metadata, not the image bytes."
    request_json POST "$ACCESS_URL/api/v1/assets/$asset_id/media/uploads" \
        "$(jq -nc --arg filename "$(basename "$MEDIA_FIXTURE")" --arg checksum "$checksum" --argjson size "$size" '{filename:$filename, contentType:"image/jpeg", sizeBytes:$size, checksumSha256:$checksum}')" \
        'content-type: application/json' "x-user-id: $ADMIN_USER" "x-org-id: $ORG_ID" "idempotency-key: $(cat /proc/sys/kernel/random/uuid)"
    assert_equals "201" "$HTTP_STATUS" "Upload session creation"
    session_body="$HTTP_BODY"
    upload_id="$(jq -er '.data.uploadId' <<<"$session_body")"
    print_json "Session response (presigned URL intentionally redacted)" "$session_body" \
        '.data | {uploadId, sessionExpiresAt, upload:{method:.upload.method, headerNames:(.upload.headers | keys), url:"private presigned URL redacted"}}'

    act "Uploading the binary directly to the private presigned MinIO URL."
    upload_binary_to_session "$session_body" "$MEDIA_FIXTURE"

    act "Committing the uploaded session. This creates the media version, processing job, and durable outbox event in the accepted transaction."
    request_json PUT "$ACCESS_URL/api/v1/assets/$asset_id/media" \
        "$(jq -nc --arg uploadId "$upload_id" '{uploadId:$uploadId}')" \
        'content-type: application/json' "x-user-id: $ADMIN_USER" "x-org-id: $ORG_ID"
    assert_equals "202" "$HTTP_STATUS" "Media commit acceptance"
    commit_body="$HTTP_BODY"
    job_id="$(jq -er '.data.jobId' <<<"$commit_body")"
    print_json "Commit response" "$commit_body" '.data | {assetId, uploadId, jobId, status, acceptedAt}'

    say "The HTTP request has finished. The durable outbox publishes to Kafka, and the isolated worker validates, renders, and promotes the completed version."
    act "Polling only worker-owned state until the job is terminal."
    completion="$(wait_for_media_completion "$job_id")"
    print_json "Durable delivery and processing evidence" "$completion"
    assert_equals "completed" "$(jq -r '.jobStatus' <<<"$completion")" "Media processing job completion"
    assert_equals "published" "$(jq -r '.outboxStatus' <<<"$completion")" "Durable outbox publication"
    assert_equals "completed" "$(jq -r '.versionStatus' <<<"$completion")" "Media version completion"
    assert_true "$(jq -r '.activeVersionId != null' <<<"$completion")" "Only a completed version was promoted active"
    assert_equals "2" "$(jq -r '.outputCount' <<<"$completion")" "Exactly two renditions were recorded"

    evidence="$(asset_db_json "
        SELECT json_build_object(
          'rawObjectKey', version.raw_object_key,
          'outputs', coalesce(json_agg(json_build_object('kind', output.kind, 'objectKey', output.object_key) ORDER BY output.kind) FILTER (WHERE output.id IS NOT NULL), '[]'::json)
        )
        FROM media_processing_jobs AS job
        JOIN asset_media_versions AS version ON version.id = job.version_id
        LEFT JOIN media_outputs AS output ON output.version_id = version.id
        WHERE job.id = '$job_id'
        GROUP BY version.raw_object_key;")"
    raw_key="$(jq -er '.rawObjectKey' <<<"$evidence")"
    mapfile -t output_keys < <(jq -r '.outputs[].objectKey' <<<"$evidence")
    act "Reading MinIO metadata for the private original and the two worker-created renditions."
    minio_stat "$raw_key" | jq --arg objectKey "$raw_key" '{objectKey:$objectKey, size:.size, contentType:.metadata."Content-Type"}'
    for output_key in "${output_keys[@]}"; do
        minio_stat "$output_key" | jq --arg objectKey "$output_key" '{objectKey:$objectKey, size:.size, contentType:.metadata."Content-Type"}'
    done
    assertion "M1 completed. Asset ID: $asset_id; job ID: $job_id. Both are printed for the evidence appendix."
}

wait_for_folder_deletion_completion() {
    local job_id="$1" deadline=$((SECONDS + JOB_TIMEOUT_SECONDS)) data status
    while (( SECONDS < deadline )); do
        data="$(graphql_data \
            'query($orgId: ID!, $id: ID!) { folderDeletionJob(orgId: $orgId, id: $id) { id status activeFolderCount activeMetadataCount deletedFolderCount deletedMetadataCount attempts lastErrorCode completedAt } }' \
            "$(jq -nc --arg org "$ORG_ID" --arg id "$job_id" '{orgId:$org, id:$id}')")"
        status="$(jq -r '.folderDeletionJob.status' <<<"$data")"
        case "$status" in
            succeeded)
                printf '%s' "$data"
                return 0
                ;;
            failed|cancelled)
                jq . <<<"$data" >&2
                die "Folder deletion job reached terminal $status state."
                ;;
        esac
        sleep 1
    done
    die "Folder deletion job $job_id did not finish within ${JOB_TIMEOUT_SECONDS}s."
}

# queue_folder_deletion performs the public preview/confirm flow and waits for
# the worker-owned deletion. Its JSON result is intentionally compact so a
# scenario can present the important evidence without reimplementing the flow.
queue_folder_deletion() {
    local folder_id="$1" preview_data preview_id confirmation_token job_data job_id completion
    preview_data="$(graphql_data \
        'mutation($orgId: ID!, $folderId: ID!) { previewFolderDeletion(orgId: $orgId, folderId: $folderId) { id rootFolderId activeFolderCount activeMetadataCount tombstoneFolderCount tombstoneMetadataCount totalRows confirmationToken expiresAt } }' \
        "$(jq -nc --arg org "$ORG_ID" --arg folderId "$folder_id" '{orgId:$org, folderId:$folderId}')")"
    preview_id="$(jq -er '.previewFolderDeletion.id' <<<"$preview_data")"
    confirmation_token="$(jq -er '.previewFolderDeletion.confirmationToken' <<<"$preview_data")"
    job_data="$(graphql_data \
        'mutation($orgId: ID!, $folderId: ID!, $previewId: ID!, $confirmationToken: String!) { confirmFolderDeletion(orgId: $orgId, folderId: $folderId, previewId: $previewId, confirmationToken: $confirmationToken) { id status rootFolderId activeFolderCount activeMetadataCount deletedFolderCount deletedMetadataCount } }' \
        "$(jq -nc --arg org "$ORG_ID" --arg folderId "$folder_id" --arg previewId "$preview_id" --arg token "$confirmation_token" '{orgId:$org, folderId:$folderId, previewId:$previewId, confirmationToken:$token}')")"
    job_id="$(jq -er '.confirmFolderDeletion.id' <<<"$job_data")"
    completion="$(wait_for_folder_deletion_completion "$job_id")"
    jq -nc --argjson preview "$preview_data" --argjson accepted "$job_data" --argjson completed "$completion" \
        '{preview:$preview.previewFolderDeletion, accepted:$accepted.confirmFolderDeletion, completed:$completed.folderDeletionJob}'
}

wait_for_lifecycle_completion() {
    local job_id="$1" deadline=$((SECONDS + JOB_TIMEOUT_SECONDS)) data status
    while (( SECONDS < deadline )); do
        data="$(graphql_data \
            'query($orgId: ID!, $jobId: ID!) { lifecycleJob(orgId: $orgId, jobId: $jobId) { jobId lifecycleUnitId operation status attempts failureCode queuedAt startedAt completedAt } }' \
            "$(jq -nc --arg org "$ORG_ID" --arg jobId "$job_id" '{orgId:$org, jobId:$jobId}')")"
        status="$(jq -r '.lifecycleJob.status' <<<"$data")"
        case "$status" in
            SUCCEEDED)
                printf '%s' "$data"
                return 0
                ;;
            FAILED|SUPPRESSED)
                jq . <<<"$data" >&2
                die "Lifecycle job reached terminal $status state."
                ;;
        esac
        sleep 1
    done
    die "Lifecycle job $job_id did not finish within ${JOB_TIMEOUT_SECONDS}s."
}

lifecycle_soft_delete_evidence() {
    local root_id="$1" child_id="$2" metadata_id="$3"
    asset_db_json "
        WITH unit AS (
            SELECT id, state, root_resource_type, root_resource_id, delete_completed_at, retention_until
            FROM asset_lifecycle_units
            WHERE org_id = '$ORG_ID' AND root_resource_id = '$root_id'
            ORDER BY created_at DESC
            LIMIT 1
        )
        SELECT json_build_object(
            'lifecycleUnit', (SELECT id FROM unit),
            'unitState', (SELECT state FROM unit),
            'rootResourceType', (SELECT root_resource_type FROM unit),
            'rootResourceId', (SELECT root_resource_id FROM unit),
            'deleteCompletedAt', (SELECT delete_completed_at FROM unit),
            'retentionUntil', (SELECT retention_until FROM unit),
            'folders', (
                SELECT coalesce(json_agg(json_build_object(
                    'id', folder.id,
                    'path', folder.path,
                    'deletedAt', folder.deleted_at,
                    'lifecycleUnitId', folder.lifecycle_unit_id
                ) ORDER BY folder.path), '[]'::json)
                FROM folders AS folder
                WHERE folder.id IN ('$root_id', '$child_id')
            ),
            'metadata', (
                SELECT coalesce(json_agg(json_build_object(
                    'id', item.id,
                    'folderId', item.folder_id,
                    'deletedAt', item.deleted_at,
                    'lifecycleUnitId', item.lifecycle_unit_id
                ) ORDER BY item.id), '[]'::json)
                FROM metadata_items AS item
                WHERE item.id = '$metadata_id'
            )
        );"
}

run_l1() {
    require_tools
    wait_http_ok "$ACCESS_URL/health" "Access Core health before L1"

    local root_data child_data root_id root_path child_id child_path metadata_id preview_data preview_id confirmation_token job_data job_id completion normal_tree normal_metadata recycle_data evidence
    local root_name child_name metadata_title
    root_name="$RUN_ID-recycle-root"
    child_name="$RUN_ID-child"
    metadata_title="$RUN_ID-document"

    say "I will delete one small folder tree. The tree has a root folder, one child folder, and one metadata item. The API should accept a durable job, hide the tree, and expose only the root in the Recycle Bin."
    act "Creating an isolated folder tree through GraphQL. Nothing in this scenario uses SQL to create or delete business data."
    root_data="$(create_folder "$root_name" "root")"
    root_id="$(jq -er '.createFolder.id' <<<"$root_data")"
    root_path="$(jq -er '.createFolder.path' <<<"$root_data")"
    child_data="$(create_folder "$child_name" "$root_path")"
    child_id="$(jq -er '.createFolder.id' <<<"$child_data")"
    child_path="$(jq -er '.createFolder.path' <<<"$child_data")"
    metadata_id="$(create_metadata_in_folder "$child_id" "$metadata_title" | jq -er '.createMetadata.id')"
    print_json "Created L1 tree" "$(jq -nc --arg rootId "$root_id" --arg rootPath "$root_path" --arg childId "$child_id" --arg childPath "$child_path" --arg metadataId "$metadata_id" '{root:{id:$rootId,path:$rootPath}, child:{id:$childId,path:$childPath}, metadata:{id:$metadataId}}')"

    say "A large or non-empty folder is not synchronously deleted. First, the server calculates a short-lived preview. Then a matching confirmation creates a durable deletion job."
    act "Requesting the deletion preview, which reports exactly what the worker will process."
    preview_data="$(graphql_data \
        'mutation($orgId: ID!, $folderId: ID!) { previewFolderDeletion(orgId: $orgId, folderId: $folderId) { id rootFolderId activeFolderCount activeMetadataCount tombstoneFolderCount tombstoneMetadataCount totalRows confirmationToken expiresAt } }' \
        "$(jq -nc --arg org "$ORG_ID" --arg folderId "$root_id" '{orgId:$org, folderId:$folderId}')")"
    preview_id="$(jq -er '.previewFolderDeletion.id' <<<"$preview_data")"
    confirmation_token="$(jq -er '.previewFolderDeletion.confirmationToken' <<<"$preview_data")"
    print_json "Deletion preview (confirmation token redacted)" "$preview_data" \
        '.previewFolderDeletion | {id, rootFolderId, activeFolderCount, activeMetadataCount, tombstoneFolderCount, tombstoneMetadataCount, totalRows, expiresAt, confirmationToken:"one-time token redacted"}'
    assert_equals "2" "$(jq -r '.previewFolderDeletion.activeFolderCount' <<<"$preview_data")" "Preview counts root and child folders"
    assert_equals "1" "$(jq -r '.previewFolderDeletion.activeMetadataCount' <<<"$preview_data")" "Preview counts the descendant metadata item"

    act "Confirming the exact preview. The public request returns a durable job instead of deleting the entire tree in the request path."
    job_data="$(graphql_data \
        'mutation($orgId: ID!, $folderId: ID!, $previewId: ID!, $confirmationToken: String!) { confirmFolderDeletion(orgId: $orgId, folderId: $folderId, previewId: $previewId, confirmationToken: $confirmationToken) { id status rootFolderId activeFolderCount activeMetadataCount deletedFolderCount deletedMetadataCount } }' \
        "$(jq -nc --arg org "$ORG_ID" --arg folderId "$root_id" --arg previewId "$preview_id" --arg token "$confirmation_token" '{orgId:$org, folderId:$folderId, previewId:$previewId, confirmationToken:$token}')")"
    job_id="$(jq -er '.confirmFolderDeletion.id' <<<"$job_data")"
    print_json "Accepted folder deletion job" "$job_data" '.confirmFolderDeletion'
    assertion "The accepted job ID is $job_id. The request path is now free; the delete worker owns the batched work."

    act "Polling the public job API until the worker finishes its bounded batches."
    completion="$(wait_for_folder_deletion_completion "$job_id")"
    print_json "Completed folder deletion job" "$completion" '.folderDeletionJob'
    assert_equals "succeeded" "$(jq -r '.folderDeletionJob.status' <<<"$completion")" "Folder deletion job completion"
    assert_equals "2" "$(jq -r '.folderDeletionJob.deletedFolderCount' <<<"$completion")" "Worker tombstoned both folder rows"
    assert_equals "1" "$(jq -r '.folderDeletionJob.deletedMetadataCount' <<<"$completion")" "Worker tombstoned the descendant metadata row"

    say "Now I prove the user-facing visibility rule. A normal folder tree query cannot see the deleted root, and an item lookup returns null rather than the hidden metadata."
    normal_tree="$(graphql_data \
        'query($orgId: ID!) { folderTree(orgId: $orgId, rootPath: "root") { id path name } }' \
        "$(jq -nc --arg org "$ORG_ID" '{orgId:$org}')")"
    assert_equals "0" "$(jq --arg rootId "$root_id" '[.folderTree[] | select(.id == $rootId)] | length' <<<"$normal_tree")" "Normal folder tree hides the deleted root"
    normal_metadata="$(graphql_data \
        'query($orgId: ID!, $id: ID!) { metadataItem(orgId: $orgId, id: $id) { id title folderId } }' \
        "$(jq -nc --arg org "$ORG_ID" --arg id "$metadata_id" '{orgId:$org, id:$id}')")"
    assert_equals "null" "$(jq -r '.metadataItem' <<<"$normal_metadata")" "Normal metadata lookup hides the tombstoned item"

    say "The Recycle Bin is a separate read model. It lists lifecycle roots, not every physical row that was hidden by the same delete action."
    recycle_data="$(graphql_data \
        'query($orgId: ID!) { recycleBin(orgId: $orgId, input: { first: 100 }) { nodes { lifecycleUnitId resourceType resourceId displayName deletedAt } pageInfo { endCursor hasNextPage } } }' \
        "$(jq -nc --arg org "$ORG_ID" '{orgId:$org}')")"
    show "L1 Recycle Bin entry"
    jq --arg rootId "$root_id" '.recycleBin.nodes | map(select(.resourceId == $rootId))' <<<"$recycle_data"
    assert_equals "1" "$(jq --arg rootId "$root_id" '[.recycleBin.nodes[] | select(.resourceId == $rootId)] | length' <<<"$recycle_data")" "Recycle Bin exposes exactly one root entry for this delete action"
    assert_equals "0" "$(jq --arg childId "$child_id" --arg metadataId "$metadata_id" '[.recycleBin.nodes[] | select(.resourceId == $childId or .resourceId == $metadataId)] | length' <<<"$recycle_data")" "Recycle Bin does not duplicate descendant rows as entries"

    evidence="$(lifecycle_soft_delete_evidence "$root_id" "$child_id" "$metadata_id")"
    print_json "Read-only lifecycle evidence from Asset DB" "$evidence"
    assert_equals "DELETED" "$(jq -r '.unitState' <<<"$evidence")" "Lifecycle unit reached DELETED state"
    assert_equals "2" "$(jq -r '.folders | length' <<<"$evidence")" "Both folder rows are linked to the same lifecycle unit"
    assert_equals "1" "$(jq -r '.metadata | length' <<<"$evidence")" "The metadata row is linked to the same lifecycle unit"
    assert_equals "true" "$(jq -r '.lifecycleUnit as $unit | [.folders[] | (.deletedAt != null and .lifecycleUnitId == $unit)] | all' <<<"$evidence")" "Folder lifecycle membership is complete"
    assert_equals "true" "$(jq -r '.lifecycleUnit as $unit | [.metadata[] | (.deletedAt != null and .lifecycleUnitId == $unit)] | all' <<<"$evidence")" "Metadata lifecycle membership is complete"
    assertion "L1 completed. Root folder: $root_id; lifecycle job: $job_id. The rows remain intentionally for later restore and retention-cleanup scenarios."
}

run_l2() {
    require_tools
    wait_http_ok "$ACCESS_URL/health" "Access Core health before L2"

    local a_data b_data c_data a_id a_path b_id b_path c_id d_id b_delete a_delete recycle_data a_unit_id b_unit_id rejected_restore_body restore_data restore_job_id completion visible_tree visible_metadata evidence
    local a_name b_name c_name d_title
    a_name="$RUN_ID-parent-a"
    b_name="$RUN_ID-independent-b"
    c_name="$RUN_ID-member-c"
    d_title="$RUN_ID-member-d"

    say "This scenario creates A with B and C beneath it, plus metadata D inside C. B is deleted independently first, so B has its own Recycle Bin entry and its own retention clock."
    act "Creating A, B, C, and D only through GraphQL."
    a_data="$(create_folder "$a_name" "root")"
    a_id="$(jq -er '.createFolder.id' <<<"$a_data")"
    a_path="$(jq -er '.createFolder.path' <<<"$a_data")"
    b_data="$(create_folder "$b_name" "$a_path")"
    b_id="$(jq -er '.createFolder.id' <<<"$b_data")"
    b_path="$(jq -er '.createFolder.path' <<<"$b_data")"
    c_data="$(create_folder "$c_name" "$a_path")"
    c_id="$(jq -er '.createFolder.id' <<<"$c_data")"
    d_id="$(create_metadata_in_folder "$c_id" "$d_title" | jq -er '.createMetadata.id')"
    print_json "Created L2 nested tree" "$(jq -nc --arg a "$a_id" --arg ap "$a_path" --arg b "$b_id" --arg bp "$b_path" --arg c "$c_id" --arg d "$d_id" '{A:{id:$a,path:$ap},B:{id:$b,path:$bp},C:{id:$c},D:{id:$d}}')"

    act "Deleting B independently through preview, confirmation, and the delete worker."
    b_delete="$(queue_folder_deletion "$b_id")"
    print_json "Independent B deletion" "$b_delete" '.completed | {id, status, deletedFolderCount, deletedMetadataCount, completedAt}'
    assert_equals "succeeded" "$(jq -r '.completed.status' <<<"$b_delete")" "Independent B deletion completed"

    act "Deleting A afterwards. B remains a nested lifecycle root rather than becoming an accidental member of A's unit."
    a_delete="$(queue_folder_deletion "$a_id")"
    print_json "Parent A deletion" "$a_delete" '.completed | {id, status, deletedFolderCount, deletedMetadataCount, completedAt}'
    assert_equals "succeeded" "$(jq -r '.completed.status' <<<"$a_delete")" "Parent A deletion completed"

    recycle_data="$(graphql_data \
        'query($orgId: ID!) { recycleBin(orgId: $orgId, input: { first: 100 }) { nodes { lifecycleUnitId resourceId displayName resourceType deletedAt } } }' \
        "$(jq -nc --arg org "$ORG_ID" '{orgId:$org}')")"
    a_unit_id="$(jq -er --arg id "$a_id" '.recycleBin.nodes[] | select(.resourceId == $id) | .lifecycleUnitId' <<<"$recycle_data")"
    b_unit_id="$(jq -er --arg id "$b_id" '.recycleBin.nodes[] | select(.resourceId == $id) | .lifecycleUnitId' <<<"$recycle_data")"
    show "Two independent Recycle Bin roots"
    jq --arg a "$a_id" --arg b "$b_id" '.recycleBin.nodes | map(select(.resourceId == $a or .resourceId == $b))' <<<"$recycle_data"
    assert_true "$(jq -n --arg a "$a_unit_id" --arg b "$b_unit_id" '$a != $b')" "A and B have separate lifecycle units"

    say "Restoring B first is unsafe because B's original parent A is still hidden. The API rejects the request before a restore worker job exists."
    act "Attempting the invalid B restore first."
    request_json POST "$ACCESS_URL/graphql" \
        "$(jq -nc --arg q 'mutation($orgId: ID!, $unitId: ID!) { restoreLifecycleUnit(orgId: $orgId, unitId: $unitId) { jobId status } }' --arg org "$ORG_ID" --arg unit "$b_unit_id" '{query:$q, variables:{orgId:$org, unitId:$unit}}')" \
        'content-type: application/json' "x-user-id: $ADMIN_USER" "x-org-id: $ORG_ID"
    assert_equals "200" "$HTTP_STATUS" "GraphQL safe-error transport"
    rejected_restore_body="$HTTP_BODY"
    print_json "Safe parent-first restore error" "$rejected_restore_body" '.errors[0] | {message, extensions}'
    assert_equals "RESTORE_PARENT_DELETED" "$(jq -r '.errors[0].extensions.code' <<<"$rejected_restore_body")" "B restore is blocked until A is restored"
    assert_equals "3013" "$(jq -r '.errors[0].extensions.number' <<<"$rejected_restore_body")" "Blocked restore uses the documented error number"
    assert_equals "0" "$(asset_db_json "SELECT COUNT(*) FROM asset_lifecycle_jobs WHERE unit_id = '$b_unit_id' AND operation = 'RESTORE';")" "Rejected B restore created no worker job"

    say "We now restore A. This queues a new lifecycle job; the worker restores A, C, and D, but never silently restores B because B was deleted separately."
    act "Queueing the parent A restore through the public GraphQL mutation."
    restore_data="$(graphql_data \
        'mutation($orgId: ID!, $unitId: ID!) { restoreLifecycleUnit(orgId: $orgId, unitId: $unitId) { jobId lifecycleUnitId operation status attempts queuedAt } }' \
        "$(jq -nc --arg org "$ORG_ID" --arg unit "$a_unit_id" '{orgId:$org, unitId:$unit}')")"
    restore_job_id="$(jq -er '.restoreLifecycleUnit.jobId' <<<"$restore_data")"
    print_json "Accepted parent restore job" "$restore_data" '.restoreLifecycleUnit'
    assert_equals "QUEUED" "$(jq -r '.restoreLifecycleUnit.status' <<<"$restore_data")" "Parent restore is asynchronous"

    act "Polling the lifecycle job owned by the worker."
    completion="$(wait_for_lifecycle_completion "$restore_job_id")"
    print_json "Completed parent restore job" "$completion" '.lifecycleJob'
    assert_equals "SUCCEEDED" "$(jq -r '.lifecycleJob.status' <<<"$completion")" "Parent restore job completion"

    visible_tree="$(graphql_data \
        'query($orgId: ID!) { folderTree(orgId: $orgId, rootPath: "root") { id path name } }' \
        "$(jq -nc --arg org "$ORG_ID" '{orgId:$org}')")"
    visible_metadata="$(graphql_data \
        'query($orgId: ID!, $id: ID!) { metadataItem(orgId: $orgId, id: $id) { id title folderId } }' \
        "$(jq -nc --arg org "$ORG_ID" --arg id "$d_id" '{orgId:$org, id:$id}')")"
    assert_equals "1" "$(jq --arg id "$a_id" '[.folderTree[] | select(.id == $id)] | length' <<<"$visible_tree")" "A is visible after its restore"
    assert_equals "1" "$(jq --arg id "$c_id" '[.folderTree[] | select(.id == $id)] | length' <<<"$visible_tree")" "C is visible after A restore"
    assert_equals "0" "$(jq --arg id "$b_id" '[.folderTree[] | select(.id == $id)] | length' <<<"$visible_tree")" "B remains hidden after A restore"
    assert_equals "$d_id" "$(jq -r '.metadataItem.id' <<<"$visible_metadata")" "D is visible after A restore"

    recycle_data="$(graphql_data \
        'query($orgId: ID!) { recycleBin(orgId: $orgId, input: { first: 100 }) { nodes { lifecycleUnitId resourceId displayName resourceType deletedAt } } }' \
        "$(jq -nc --arg org "$ORG_ID" '{orgId:$org}')")"
    assert_equals "0" "$(jq --arg id "$a_id" '[.recycleBin.nodes[] | select(.resourceId == $id)] | length' <<<"$recycle_data")" "A leaves the Recycle Bin after restore"
    assert_equals "1" "$(jq --arg id "$b_id" '[.recycleBin.nodes[] | select(.resourceId == $id)] | length' <<<"$recycle_data")" "B remains an independent Recycle Bin root"

    evidence="$(asset_db_json "
        SELECT json_build_object(
          'parentUnitState', (SELECT state FROM asset_lifecycle_units WHERE id = '$a_unit_id'),
          'nestedUnitState', (SELECT state FROM asset_lifecycle_units WHERE id = '$b_unit_id'),
          'aVisible', (SELECT deleted_at IS NULL AND lifecycle_unit_id IS NULL FROM folders WHERE id = '$a_id'),
          'bStillDeleted', (SELECT deleted_at IS NOT NULL AND lifecycle_unit_id::text = '$b_unit_id' FROM folders WHERE id = '$b_id'),
          'dVisible', (SELECT deleted_at IS NULL AND lifecycle_unit_id IS NULL FROM metadata_items WHERE id = '$d_id')
        );")"
    print_json "Read-only nested lifecycle evidence" "$evidence"
    assert_equals "RESTORED" "$(jq -r '.parentUnitState' <<<"$evidence")" "A lifecycle unit is closed as restored"
    assert_equals "DELETED" "$(jq -r '.nestedUnitState' <<<"$evidence")" "B lifecycle unit keeps its own deletion state"
    assert_true "$(jq -r '.aVisible' <<<"$evidence")" "A is physically marked active"
    assert_true "$(jq -r '.bStillDeleted' <<<"$evidence")" "B remains physically tombstoned in its own unit"
    assert_true "$(jq -r '.dVisible' <<<"$evidence")" "D is physically marked active"
    assertion "L2 completed. Parent A restore job: $restore_job_id. Nested B remains a separate Recycle Bin entry and keeps its retention clock."
}

run_r1() {
    require_tools
    [[ -f "$MEDIA_FIXTURE" ]] || die "R1 image fixture does not exist: $MEDIA_FIXTURE"
    wait_http_ok "$ACCESS_URL/health" "Access Core health before R1"
    assert_r1_daily_slot_available

    local root_data root_id root_path asset_id size checksum session_body upload_id commit_body media_job_id media_completion media_evidence raw_key output_key
    local deletion unit_id retention_until cleanup_run purge_job purge_completion quota_before quota_after final_evidence expected_stored_bytes actual_stored_bytes
    local -a output_keys
    local root_name asset_title
    root_name="$RUN_ID-purge-root"
    asset_title="$RUN_ID-purge-media"

    say "R1 proves the 30-day cleanup path without waiting thirty days. I create a private media asset normally, delete it through the public folder workflow, and advance only this isolated fixture's retention clock. The scheduler and workers remain real."
    act "Creating an isolated folder and metadata asset through GraphQL."
    root_data="$(create_folder "$root_name" "root")"
    root_id="$(jq -er '.createFolder.id' <<<"$root_data")"
    root_path="$(jq -er '.createFolder.path' <<<"$root_data")"
    asset_id="$(create_metadata_in_folder "$root_id" "$asset_title" '{"phase":"1.3","scenario":"R1"}' | jq -er '.createMetadata.id')"
    print_json "Created R1 fixture" "$(jq -nc --arg rootId "$root_id" --arg rootPath "$root_path" --arg assetId "$asset_id" '{root:{id:$rootId,path:$rootPath},metadataAssetId:$assetId}')"

    size="$(wc -c < "$MEDIA_FIXTURE" | tr -d '[:space:]')"
    checksum="$(openssl dgst -sha256 -binary "$MEDIA_FIXTURE" | base64 | tr -d '\n')"
    act "Creating and committing a real direct-upload session for the R1 asset."
    request_json POST "$ACCESS_URL/api/v1/assets/$asset_id/media/uploads" \
        "$(jq -nc --arg filename "$(basename "$MEDIA_FIXTURE")" --arg checksum "$checksum" --argjson size "$size" '{filename:$filename, contentType:"image/jpeg", sizeBytes:$size, checksumSha256:$checksum}')" \
        'content-type: application/json' "x-user-id: $ADMIN_USER" "x-org-id: $ORG_ID" "idempotency-key: $(cat /proc/sys/kernel/random/uuid)"
    assert_equals "201" "$HTTP_STATUS" "R1 upload session creation"
    session_body="$HTTP_BODY"
    upload_id="$(jq -er '.data.uploadId' <<<"$session_body")"
    upload_binary_to_session "$session_body" "$MEDIA_FIXTURE"
    request_json PUT "$ACCESS_URL/api/v1/assets/$asset_id/media" \
        "$(jq -nc --arg uploadId "$upload_id" '{uploadId:$uploadId}')" \
        'content-type: application/json' "x-user-id: $ADMIN_USER" "x-org-id: $ORG_ID"
    assert_equals "202" "$HTTP_STATUS" "R1 media commit acceptance"
    commit_body="$HTTP_BODY"
    media_job_id="$(jq -er '.data.jobId' <<<"$commit_body")"
    media_completion="$(wait_for_media_completion "$media_job_id")"
    assert_equals "completed" "$(jq -r '.jobStatus' <<<"$media_completion")" "R1 media processing completion"

    media_evidence="$(asset_db_json "
        SELECT json_build_object(
            'rawObjectKey', version.raw_object_key,
            'storedBytes', version.original_size_bytes,
            'outputKeys', coalesce(json_agg(output.object_key ORDER BY output.object_key) FILTER (WHERE output.id IS NOT NULL), '[]'::json)
        )
        FROM asset_media_versions AS version
        LEFT JOIN media_outputs AS output ON output.version_id = version.id
        WHERE version.asset_id = '$asset_id'
        GROUP BY version.raw_object_key, version.original_size_bytes;")"
    raw_key="$(jq -er '.rawObjectKey' <<<"$media_evidence")"
    mapfile -t output_keys < <(jq -r '.outputKeys[]' <<<"$media_evidence")
    quota_before="$(asset_db_json "SELECT json_build_object('storedRawBytes', stored_raw_bytes, 'reservedRawBytes', reserved_raw_bytes) FROM organization_media_usage WHERE org_id = '$ORG_ID';")"
    print_json "R1 media evidence before deletion" "$(jq -nc --argjson media "$media_evidence" --argjson quota "$quota_before" '{media:$media,quotaBefore:$quota}')"
    minio_stat "$raw_key" >/dev/null
    for output_key in "${output_keys[@]}"; do minio_stat "$output_key" >/dev/null; done
    assertion "The original and every rendition exist before deletion, and the quota ledger records the raw bytes."

    say "The user-facing delete is still asynchronous. The folder worker tombstones the media asset and creates one DELETED lifecycle unit; nothing is physically removed yet."
    act "Deleting the R1 folder through preview, confirmation, and the existing delete worker."
    deletion="$(queue_folder_deletion "$root_id")"
    print_json "R1 completed soft-delete job" "$deletion" '.completed | {id,status,deletedFolderCount,deletedMetadataCount,completedAt}'
    assert_equals "succeeded" "$(jq -r '.completed.status' <<<"$deletion")" "R1 folder deletion completion"
    assert_equals "1" "$(jq -r '.completed.deletedMetadataCount' <<<"$deletion")" "R1 worker tombstoned the media asset"

    unit_id="$(asset_db_json "SELECT id FROM asset_lifecycle_units WHERE org_id = '$ORG_ID' AND root_resource_id = '$root_id' AND state = 'DELETED' ORDER BY created_at DESC LIMIT 1;")"
    [[ -n "$unit_id" ]] || die "R1 did not create a DELETED lifecycle unit for root $root_id."
    act "Advancing only the isolated fixture's retention clock. This does not create a run or a PURGE job."
    retention_until="$(expire_retention_fixture "$unit_id")"
    show "Expired R1 fixture retention"
    jq -nc --arg lifecycleUnitId "$unit_id" --arg retentionUntil "$retention_until" '{lifecycleUnitId:$lifecycleUnitId,retentionUntil:$retentionUntil}'

    say "Now the controlled scheduler action runs the exact daily lease and run-record path. The scheduler only makes work available; the worker independently selects the expired root and queues PURGE."
    act "Running the retention scheduler once without waiting for 02:00."
    "${COMPOSE[@]}" run --rm --no-deps asset-retention-scheduler --run-once
    cleanup_run="$(wait_for_cleanup_run)"
    print_json "Daily cleanup run created through the scheduler lease" "$cleanup_run"
    purge_job="$(wait_for_purge_job "$unit_id")"
    print_json "Worker-owned PURGE job" "$purge_job"
    assert_equals "PURGE" "$(jq -r '.operation' <<<"$purge_job")" "Cleanup worker queued a PURGE job"

    act "Waiting for the lifecycle worker to delete MinIO objects, release quota, delete asset rows, then delete folders leaf-first."
    # PURGE removes the authorization root itself. Because public job-status
    # lookup is authorized against the current resource, it is not the right
    # post-purge evidence source. The demo reads the durable job row, then
    # proves physical absence below.
    purge_completion="$(wait_for_purge_completion "$unit_id")"
    print_json "Completed physical PURGE job" "$purge_completion"
    assert_equals "SUCCEEDED" "$(jq -r '.status' <<<"$purge_completion")" "Physical PURGE job completion"

    quota_after="$(asset_db_json "SELECT json_build_object('storedRawBytes', stored_raw_bytes, 'reservedRawBytes', reserved_raw_bytes) FROM organization_media_usage WHERE org_id = '$ORG_ID';")"
    final_evidence="$(asset_db_json "
        SELECT json_build_object(
            'lifecycleUnitState', (SELECT state FROM asset_lifecycle_units WHERE id = '$unit_id'),
            'metadataRowsRemaining', (SELECT count(*) FROM metadata_items WHERE id = '$asset_id'),
            'folderRowsRemaining', (SELECT count(*) FROM folders WHERE id = '$root_id'),
            'mediaVersionRowsRemaining', (SELECT count(*) FROM asset_media_versions WHERE asset_id = '$asset_id'),
            'mediaOutputRowsRemaining', (SELECT count(*) FROM media_outputs WHERE version_id IN (SELECT id FROM asset_media_versions WHERE asset_id = '$asset_id')),
            'uploadSessionRowsRemaining', (SELECT count(*) FROM media_upload_sessions WHERE asset_id = '$asset_id')
        );")"
    print_json "R1 physical-purge evidence" "$(jq -nc --argjson final "$final_evidence" --argjson quotaBefore "$quota_before" --argjson quotaAfter "$quota_after" '{physicalRows:$final,quotaBefore:$quotaBefore,quotaAfter:$quotaAfter}')"
    assert_equals "PURGED" "$(jq -r '.lifecycleUnitState' <<<"$final_evidence")" "Lifecycle unit reaches PURGED only after physical teardown"
    assert_equals "0" "$(jq -r '.metadataRowsRemaining' <<<"$final_evidence")" "Asset metadata row was physically deleted"
    assert_equals "0" "$(jq -r '.folderRowsRemaining' <<<"$final_evidence")" "Folder row was physically deleted"
    assert_equals "0" "$(jq -r '.mediaVersionRowsRemaining' <<<"$final_evidence")" "Media-version rows were physically deleted"
    assert_equals "0" "$(jq -r '.mediaOutputRowsRemaining' <<<"$final_evidence")" "Rendition rows were physically deleted"
    assert_equals "0" "$(jq -r '.uploadSessionRowsRemaining' <<<"$final_evidence")" "Upload-session rows were physically deleted"

    expected_stored_bytes=$(( $(jq -er '.storedRawBytes' <<<"$quota_before") - $(jq -er '.storedBytes' <<<"$media_evidence") ))
    actual_stored_bytes="$(jq -er '.storedRawBytes' <<<"$quota_after")"
    assert_equals "$expected_stored_bytes" "$actual_stored_bytes" "Raw-byte quota was released exactly once"
    assert_equals "0" "$(jq -er '.reservedRawBytes' <<<"$quota_after")" "No upload reservation remains after purge"
    act "Checking MinIO after physical purge. A successful stat would fail this scenario."
    assert_minio_object_absent "$raw_key"
    for output_key in "${output_keys[@]}"; do assert_minio_object_absent "$output_key"; done
    assertion "R1 completed. The scheduler recorded one daily run, the worker owned PURGE, and every asset row/object from this isolated fixture was removed."
}

if [[ "$LIST_ONLY" -eq 1 ]]; then
    scenario_catalog
    exit 0
fi

if [[ "$SETUP" -eq 1 ]]; then
    [[ "$INTERACTIVE" -eq 0 && "$NON_INTERACTIVE" -eq 0 && "${#SCENARIOS[@]}" -eq 0 ]] || \
        die "--setup is a standalone, non-destructive preparation command. Run the selected scenario afterwards."
    setup_release_stack
    exit 0
fi

[[ "$INTERACTIVE" -ne "$NON_INTERACTIVE" ]] || die "Specify exactly one of --interactive or --non-interactive."
[[ "${#SCENARIOS[@]}" -gt 0 ]] || die "Specify at least one --scenario ID, for example --scenario M1."

for scenario_id in "${SCENARIOS[@]}"; do
    case "$scenario_id" in
        M1) run_m1 ;;
        L1) run_l1 ;;
        L2) run_l2 ;;
        R1) run_r1 ;;
        E1|E2|E3|E4|E5|E6)
            die "$scenario_id is in the catalogue but is not implemented in this delivery slice yet."
            ;;
        *) die "Unknown scenario ID: $scenario_id" ;;
    esac
done
