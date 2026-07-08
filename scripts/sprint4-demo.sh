#!/usr/bin/env bash
# Linux/macOS port of sprint4-demo.ps1.
#
# Runs the Sprint 4 / Final Demo Runbook (FD-00..FD-10) with shared assertions
# for CI-like (--non-interactive) and rehearsal (--interactive) modes. Resets
# the two project databases, builds and starts exact Node and Go binaries,
# executes FD-00 through FD-10, and proves cleanup. Destructive volume reset
# is always explicit via --approve-destructive-reset.
#
# Usage:
#   ./sprint4-demo.sh (--interactive|--non-interactive) \
#       --open-images-directory DIR --approve-destructive-reset \
#       [--keep-environment] [--failure-injection None|AfterBoot] \
#       [--readiness-timeout-seconds N]
set -uo pipefail

INTERACTIVE=""
NON_INTERACTIVE=""
OPEN_IMAGES_DIR=""
APPROVE_DESTRUCTIVE_RESET=0
KEEP_ENVIRONMENT=0
FAILURE_INJECTION="None"
READINESS_TIMEOUT_SECONDS=60

while [[ $# -gt 0 ]]; do
    case "$1" in
        --interactive) INTERACTIVE=1; shift ;;
        --non-interactive) NON_INTERACTIVE=1; shift ;;
        --open-images-directory) OPEN_IMAGES_DIR="$2"; shift 2 ;;
        --approve-destructive-reset) APPROVE_DESTRUCTIVE_RESET=1; shift ;;
        --keep-environment) KEEP_ENVIRONMENT=1; shift ;;
        --failure-injection) FAILURE_INJECTION="$2"; shift 2 ;;
        --readiness-timeout-seconds) READINESS_TIMEOUT_SECONDS="$2"; shift 2 ;;
        *) echo "Unknown argument: $1" >&2; exit 1 ;;
    esac
done

if [[ "${INTERACTIVE:-0}" == "${NON_INTERACTIVE:-0}" ]]; then
    echo "Specify exactly one of --interactive or --non-interactive" >&2
    exit 1
fi
if [[ -z "$OPEN_IMAGES_DIR" ]]; then
    echo "--open-images-directory is required" >&2
    exit 1
fi
if [[ "$APPROVE_DESTRUCTIVE_RESET" -ne 1 ]]; then
    echo "--approve-destructive-reset is required because the demo deletes project database volumes" >&2
    exit 1
fi
if [[ "$KEEP_ENVIRONMENT" -eq 1 && "$FAILURE_INJECTION" != "None" ]]; then
    echo "Failure injection cannot keep the environment" >&2
    exit 1
fi
if [[ "$FAILURE_INJECTION" != "None" && "$FAILURE_INJECTION" != "AfterBoot" ]]; then
    echo "--failure-injection must be None or AfterBoot" >&2
    exit 1
fi
if [[ "$READINESS_TIMEOUT_SECONDS" -lt 10 || "$READINESS_TIMEOUT_SECONDS" -gt 180 ]]; then
    echo "--readiness-timeout-seconds must be between 10 and 180" >&2
    exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
ACCESS_CORE="$REPO_ROOT/services/access-core"
ASSET_CORE="$REPO_ROOT/services/asset-core"

RUN_ID="s4demo-$(date -u +%Y%m%dT%H%M%SZ)-$$"
TEMP_DIR="${TMPDIR:-/tmp}/$RUN_ID"
NODE_STDOUT="$TEMP_DIR/node.stdout.log"
NODE_STDERR="$TEMP_DIR/node.stderr.log"
GO_STDOUT="$TEMP_DIR/go.stdout.log"
GO_STDERR="$TEMP_DIR/go.stderr.log"
GO_BINARY="$TEMP_DIR/asset-core"
IMPORT_BINARY="$TEMP_DIR/import-sample"

NODE_PID=""
GO_PID=""
NODE_PORT=4000
GO_PORT=8080
ADMIN_USER="00000000-0000-0000-0000-000000000001"
VIEWER_USER="00000000-0000-0000-0000-000000000002"
UNKNOWN_USER="99999999-9999-9999-9999-999999999999"
ORG_ID="00000000-0000-0000-0000-000000000010"
OTHER_ORG_ID="00000000-0000-0000-0000-000000000020"

PRIMARY_ERROR=""
CLEANUP_ERROR=""
DEMO_SUCCEEDED=0

# ---------- helpers ----------

log()   { printf '%s\n' "$*"; }
die()   { PRIMARY_ERROR="$*"; return 1; }

scenario() {
    local id="$1" title="$2"
    printf '\n\033[36m=== %s: %s ===\033[0m\n' "$id" "$title"
    if [[ -n "$INTERACTIVE" ]]; then
        read -r -p "Press Enter to continue" _ || true
    fi
}

assert_equal() {
    local expected="$1" actual="$2" message="$3"
    if [[ "$expected" != "$actual" ]]; then
        die "$message. Expected: $expected; actual: $actual"
        return 1
    fi
}

assert_port_free() {
    local port="$1"
    if ss -H -ltn "sport = :$port" 2>/dev/null | grep -q .; then
        local pid
        pid=$(lsof -tiTCP:"$port" -sTCP:LISTEN 2>/dev/null | tr '\n' ',' | sed 's/,$//')
        die "Port $port is occupied by PID ${pid:-unknown}"
        return 1
    fi
}

# wait_service NAME URL PID LOG...
wait_service() {
    local name="$1" url="$2" pid="$3"; shift 3
    local logs=("$@")
    local deadline=$((SECONDS + READINESS_TIMEOUT_SECONDS))
    while (( SECONDS < deadline )); do
        if ! kill -0 "$pid" 2>/dev/null; then
            for l in "${logs[@]}"; do [[ -f "$l" ]] && tail -n 100 "$l"; done
            die "$name exited before readiness"
            return 1
        fi
        if curl -sf -m 1 "$url" -o /dev/null; then
            return 0
        fi
        sleep 0.5
    done
    for l in "${logs[@]}"; do [[ -f "$l" ]] && tail -n 100 "$l"; done
    die "$name readiness timed out after $READINESS_TIMEOUT_SECONDS seconds"
    return 1
}

# graphql_raw QUERY VARIABLES_JSON USER_ID ORG_ID -> prints raw response JSON
graphql_raw() {
    local query="$1" variables="${2-}" user_id="${3:-}" org_id="${4:-}"
    if [[ -z "$variables" ]]; then variables='{}'; fi
    local args=(-s -X POST "http://127.0.0.1:$NODE_PORT/graphql" -H "Content-Type: application/json")
    [[ -n "$user_id" ]] && args+=(-H "x-user-id: $user_id")
    [[ -n "$org_id" ]] && args+=(-H "x-org-id: $org_id")
    local body
    body=$(jq -nc --arg q "$query" --argjson v "$variables" '{query:$q, variables:$v}')
    curl "${args[@]}" -d "$body"
}

# graphql QUERY VARIABLES_JSON USER_ID ORG_ID -> prints .data JSON, dies on GraphQL error
graphql() {
    local response
    response=$(graphql_raw "$@")
    if ! jq -e . >/dev/null 2>&1 <<<"$response"; then
        die "GraphQL request did not return valid JSON: $response"
        return 1
    fi
    local has_errors has_data
    has_errors=$(jq -r 'has("errors") and (.errors | length > 0)' <<<"$response")
    has_data=$(jq -r 'has("data")' <<<"$response")
    if [[ "$has_errors" == "true" ]]; then
        local code msg
        code=$(jq -r '.errors[0].extensions.code // "UNKNOWN"' <<<"$response")
        msg=$(jq -r '.errors[0].message // "unknown"' <<<"$response")
        die "Unexpected GraphQL error [$code]: $msg"
        return 1
    fi
    if [[ "$has_data" != "true" ]]; then
        die "GraphQL response had neither data nor errors: $response"
        return 1
    fi
    jq -c '.data' <<<"$response"
}

# assert_graphql_error SCENARIO_ID EXPECTED_CODE QUERY VARIABLES_JSON USER_ID ORG_ID
assert_graphql_error() {
    local scenario_id="$1" expected_code="$2" query="$3" variables="${4-}" user_id="${5:-}" org_id="${6:-}"
    if [[ -z "$variables" ]]; then variables='{}'; fi
    local response
    response=$(graphql_raw "$query" "$variables" "$user_id" "$org_id")
    local has_errors
    has_errors=$(jq -r 'has("errors") and (.errors | length > 0)' <<<"$response")
    if [[ "$has_errors" != "true" ]]; then
        die "$scenario_id expected GraphQL error $expected_code but request succeeded"
        return 1
    fi
    local actual_code
    actual_code=$(jq -r '.errors[0].extensions.code // "UNKNOWN"' <<<"$response")
    assert_equal "$expected_code" "$actual_code" "$scenario_id returned the wrong GraphQL code"
}

# invoke_psql CONTAINER USER DATABASE SQL -> prints last line of output, trimmed
invoke_psql() {
    local container="$1" user="$2" database="$3" sql="$4"
    docker exec "$container" psql -U "$user" -d "$database" -Atc "$sql" | tail -n 1 | tr -d '\r'
}

set_olp() {
    local enabled="$1" value
    value=$([[ "$enabled" == "1" ]] && echo true || echo false)
    invoke_psql "seta-access-db" "access_user" "access_db" \
        "UPDATE access.organizations SET olp_enabled=$value WHERE id='$ORG_ID'; SELECT olp_enabled FROM access.organizations WHERE id='$ORG_ID';" >/dev/null
}

get_namespace_counts() {
    local folders metadata permissions
    folders=$(invoke_psql "seta-asset-db" "asset_user" "asset_db" "SELECT COUNT(*) FROM folders WHERE name LIKE '$RUN_ID%';")
    metadata=$(invoke_psql "seta-asset-db" "asset_user" "asset_db" "SELECT COUNT(*) FROM metadata_items WHERE title LIKE '$RUN_ID%';")
    permissions=$(invoke_psql "seta-access-db" "access_user" "access_db" "SELECT COUNT(*) FROM access.object_permissions;")
    echo "$folders $metadata $permissions"
}

get_metadata_hash() {
    invoke_psql "seta-asset-db" "asset_user" "asset_db" \
        "SELECT md5(COALESCE(string_agg(row_to_json(t)::text, '' ORDER BY id), '')) FROM (SELECT * FROM metadata_items) t;"
}

# grant_permission RESOURCE_TYPE RESOURCE_ID ACTION GRANTEE_USER [ACTOR] -> prints grant id
grant_permission() {
    local resource_type="$1" resource_id="$2" action="$3" grantee_user="$4" actor="${5:-$ADMIN_USER}"
    local mutation='mutation($orgId: ID!, $resourceType: ResourceType!, $resourceId: ID!, $action: PermissionAction!, $granteeUserId: ID!) { grantObjectPermission(orgId: $orgId, resourceType: $resourceType, resourceId: $resourceId, action: $action, granteeUserId: $granteeUserId) { id } }'
    local vars data
    vars=$(jq -nc --arg org "$ORG_ID" --arg rt "$resource_type" --arg rid "$resource_id" --arg act "$action" --arg gu "$grantee_user" \
        '{orgId:$org, resourceType:$rt, resourceId:$rid, action:$act, granteeUserId:$gu}')
    data=$(graphql "$mutation" "$vars" "$actor" "$ORG_ID") || return 1
    jq -r '.grantObjectPermission.id' <<<"$data"
}

# revoke_permission PERMISSION_ID [ACTOR]
revoke_permission() {
    local permission_id="$1" actor="${2:-$ADMIN_USER}"
    local mutation='mutation($id: ID!) { revokeObjectPermission(id: $id) }'
    local vars data
    vars=$(jq -nc --arg id "$permission_id" '{id:$id}')
    data=$(graphql "$mutation" "$vars" "$actor" "$ORG_ID") || return 1
    local ok
    ok=$(jq -r '.revokeObjectPermission' <<<"$data")
    assert_equal "true" "$ok" "Permission revoke failed"
}

cleanup() {
    if [[ "$KEEP_ENVIRONMENT" -ne 1 ]]; then
        {
            for pid in "$NODE_PID" "$GO_PID"; do
                if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
                    kill "$pid" 2>/dev/null || true
                    wait "$pid" 2>/dev/null || true
                fi
            done
            (cd "$REPO_ROOT" && npm run clean:all) || { CLEANUP_ERROR="Final volume cleanup failed"; }
            rm -rf "$TEMP_DIR"
            for port in "$NODE_PORT" "$GO_PORT"; do
                if ss -H -ltn "sport = :$port" 2>/dev/null | grep -q .; then
                    CLEANUP_ERROR="Cleanup left a listener on port $port"
                fi
            done
            local volumes
            volumes=$(docker volume ls --format '{{.Name}}' | grep -E '^seta-dam_(asset|access)_db_data$' || true)
            if [[ -n "$volumes" ]]; then
                CLEANUP_ERROR="Cleanup left project volumes: $(echo "$volumes" | tr '\n' ',' | sed 's/,$//')"
            fi
        }
    fi

    if [[ -n "$PRIMARY_ERROR" ]]; then
        if [[ -n "$CLEANUP_ERROR" ]]; then
            echo "$PRIMARY_ERROR; cleanup also failed: $CLEANUP_ERROR" >&2
        else
            echo "$PRIMARY_ERROR" >&2
        fi
        exit 1
    fi
    if [[ -n "$CLEANUP_ERROR" ]]; then
        echo "$CLEANUP_ERROR" >&2
        exit 1
    fi
    if [[ "$DEMO_SUCCEEDED" -ne 1 ]]; then
        echo "Demo ended without a success verdict" >&2
        exit 1
    fi
}
trap cleanup EXIT

run() {
    mkdir -p "$TEMP_DIR"
    cd "$REPO_ROOT"

    scenario "FD-00" "Preflight and trusted fixture"
    assert_port_free "$NODE_PORT" || return 1
    assert_port_free "$GO_PORT" || return 1
    if [[ ! -f "$OPEN_IMAGES_DIR/provenance-manifest.json" ]]; then
        die "Open Images manifest is missing"; return 1
    fi
    "$SCRIPT_DIR/fetch_open_images_metadata.sh" --verify-only --output-dir "$OPEN_IMAGES_DIR" || { die "Open Images fixture verification failed"; return 1; }

    scenario "FD-01" "Clean migration and exact service boot"
    npm run clean:all || { die "Initial volume reset failed"; return 1; }
    npm run docker:up || { die "Database startup failed"; return 1; }
    npm run docker:migrate || { die "Flyway migration failed"; return 1; }
    assert_equal "2" "$(invoke_psql "seta-asset-db" "asset_user" "asset_db" "SELECT MAX(version) FROM flyway_schema_history;")" "Asset Flyway version" || return 1
    assert_equal "2" "$(invoke_psql "seta-access-db" "access_user" "access_db" "SELECT MAX(version) FROM flyway_schema_history;")" "Access Flyway version" || return 1
    read -r base_folders base_metadata base_permissions < <(get_namespace_counts)
    assert_equal "0" "$base_folders" "FD-01 folder namespace must start empty" || return 1
    assert_equal "0" "$base_metadata" "FD-01 metadata namespace must start empty" || return 1
    assert_equal "0" "$base_permissions" "FD-01 permission table must start empty" || return 1

    npm --prefix services/access-core run build || { die "Node build failed"; return 1; }
    (cd "$ASSET_CORE" && go build -o "$GO_BINARY" ./cmd/server/main.go) || { die "Go server build failed"; return 1; }
    (cd "$ASSET_CORE" && go build -o "$IMPORT_BINARY" ./cmd/import-sample/main.go) || { die "Import CLI build failed"; return 1; }

    ASSET_DB_HOST=127.0.0.1 ASSET_DB_PORT=5433 ASSET_DB_NAME=asset_db ASSET_DB_USER=asset_user \
        ASSET_DB_PASSWORD=asset_password PORT="$GO_PORT" \
        "$GO_BINARY" >"$GO_STDOUT" 2>"$GO_STDERR" &
    GO_PID=$!
    wait_service "Go Asset Core" "http://127.0.0.1:$GO_PORT/healthz" "$GO_PID" "$GO_STDOUT" "$GO_STDERR" || return 1

    ACCESS_DB_HOST=127.0.0.1 ACCESS_DB_PORT=5434 ACCESS_DB_NAME=access_db ACCESS_DB_USER=access_user \
        ACCESS_DB_PASSWORD=access_password DATABASE_URL="postgresql://access_user:access_password@127.0.0.1:5434/access_db" \
        GO_ASSET_URL="http://127.0.0.1:$GO_PORT" PORT="$NODE_PORT" \
        node "$ACCESS_CORE/dist/index.js" >"$NODE_STDOUT" 2>"$NODE_STDERR" &
    NODE_PID=$!
    wait_service "Node Access Core" "http://127.0.0.1:$NODE_PORT/health" "$NODE_PID" "$NODE_STDOUT" "$NODE_STDERR" || return 1

    local schema_data
    schema_data=$(graphql '{ __schema { queryType { name } } }' '{}' "" "") || return 1
    assert_equal "Query" "$(jq -r '.__schema.queryType.name' <<<"$schema_data")" "GraphQL introspection" || return 1
    if [[ "$FAILURE_INJECTION" == "AfterBoot" ]]; then
        die "CONTROLLED_FAILURE_AFTER_BOOT"; return 1
    fi

    scenario "FD-02" "Authentication and organization isolation"
    local folder_count_before_deny
    folder_count_before_deny=$(invoke_psql "seta-asset-db" "asset_user" "asset_db" "SELECT COUNT(*) FROM folders;")
    local org_query='query { organizations { id } }'
    assert_graphql_error "AUTH-01" "UNAUTHENTICATED" "$org_query" || return 1
    assert_graphql_error "AUTH-02" "UNAUTHENTICATED" "$org_query" '{}' "$UNKNOWN_USER" "$ORG_ID" || return 1
    local tree_query='query($orgId: ID!) { folderTree(orgId: $orgId) { id name } }'
    assert_graphql_error "ORG-01" "FORBIDDEN" "$tree_query" "$(jq -nc --arg o "$OTHER_ORG_ID" '{orgId:$o}')" "$ADMIN_USER" "$OTHER_ORG_ID" || return 1
    assert_graphql_error "ORG-02" "FORBIDDEN" "$tree_query" "$(jq -nc --arg o "$OTHER_ORG_ID" '{orgId:$o}')" "$VIEWER_USER" "$OTHER_ORG_ID" || return 1
    local allowed_tree
    allowed_tree=$(graphql "$tree_query" "$(jq -nc --arg o "$ORG_ID" '{orgId:$o}')" "$ADMIN_USER" "$ORG_ID") || return 1
    if [[ "$(jq '.folderTree | length' <<<"$allowed_tree")" -lt 1 ]]; then
        die "ORG-03 expected seeded folders"; return 1
    fi
    assert_equal "$folder_count_before_deny" "$(invoke_psql "seta-asset-db" "asset_user" "asset_db" "SELECT COUNT(*) FROM folders;")" "FD-02 deny cases changed Asset DB" || return 1

    scenario "FD-03" "Folder lifecycle"
    local create_folder='mutation($orgId: ID!, $name: String!, $parentPath: String) { createFolder(orgId: $orgId, name: $name, parentPath: $parentPath) { id name path } }'
    local update_folder='mutation($orgId: ID!, $id: ID!, $name: String) { updateFolder(orgId: $orgId, id: $id, name: $name) { id name path } }'
    local move_folder='mutation($orgId: ID!, $id: ID!, $destinationParentId: ID) { moveFolder(orgId: $orgId, id: $id, destinationParentId: $destinationParentId) { id path } }'
    local delete_folder='mutation($orgId: ID!, $id: ID!) { deleteFolder(orgId: $orgId, id: $id) }'

    local root_data root_id root_path
    root_data=$(graphql "$create_folder" "$(jq -nc --arg o "$ORG_ID" --arg n "$RUN_ID-root" '{orgId:$o, name:$n, parentPath:null}')" "$ADMIN_USER" "$ORG_ID") || return 1
    root_id=$(jq -r '.createFolder.id' <<<"$root_data")
    root_path=$(jq -r '.createFolder.path' <<<"$root_data")

    local child_data child_id child_path
    child_data=$(graphql "$create_folder" "$(jq -nc --arg o "$ORG_ID" --arg n "$RUN_ID-child" --arg p "$root_path" '{orgId:$o, name:$n, parentPath:$p}')" "$ADMIN_USER" "$ORG_ID") || return 1
    child_id=$(jq -r '.createFolder.id' <<<"$child_data")
    child_path=$(jq -r '.createFolder.path' <<<"$child_data")

    assert_graphql_error "FOLDER-NONEMPTY" "CONFLICT" "$delete_folder" "$(jq -nc --arg o "$ORG_ID" --arg i "$root_id" '{orgId:$o, id:$i}')" "$ADMIN_USER" "$ORG_ID" || return 1

    local renamed_data renamed_path
    renamed_data=$(graphql "$update_folder" "$(jq -nc --arg o "$ORG_ID" --arg i "$child_id" --arg n "$RUN_ID-child-renamed" '{orgId:$o, id:$i, name:$n}')" "$ADMIN_USER" "$ORG_ID") || return 1
    renamed_path=$(jq -r '.updateFolder.path' <<<"$renamed_data")
    assert_equal "$child_path" "$renamed_path" "Folder rename changed UUID path" || return 1

    local moved_data moved_path
    moved_data=$(graphql "$move_folder" "$(jq -nc --arg o "$ORG_ID" --arg i "$child_id" '{orgId:$o, id:$i, destinationParentId:null}')" "$ADMIN_USER" "$ORG_ID") || return 1
    moved_path=$(jq -r '.moveFolder.path' <<<"$moved_data")
    if [[ "$moved_path" == "$child_path" ]]; then die "Folder move did not change path"; return 1; fi

    scenario "FD-04" "Metadata lifecycle and search"
    local create_metadata='mutation($orgId: ID!, $input: CreateMetadataInput!) { createMetadata(orgId: $orgId, input: $input) { id title } }'
    local update_metadata='mutation($orgId: ID!, $id: ID!, $input: UpdateMetadataInput!) { updateMetadata(orgId: $orgId, id: $id, input: $input) { id title description } }'
    local search_metadata='query($orgId: ID!, $input: MetadataSearchInput!) { searchMetadata(orgId: $orgId, input: $input) { id title externalId } }'
    local delete_metadata='mutation($orgId: ID!, $id: ID!) { deleteMetadata(orgId: $orgId, id: $id) }'

    local metadata_data metadata_id
    metadata_data=$(graphql "$create_metadata" "$(jq -nc --arg o "$ORG_ID" --arg f "$root_id" --arg t "$RUN_ID-metadata" '{orgId:$o, input:{folderId:$f, title:$t, metadataJson:"{\"demo\":true}"}}')" "$ADMIN_USER" "$ORG_ID") || return 1
    metadata_id=$(jq -r '.createMetadata.id' <<<"$metadata_data")

    local updated_metadata updated_title
    updated_metadata=$(graphql "$update_metadata" "$(jq -nc --arg o "$ORG_ID" --arg i "$metadata_id" --arg t "$RUN_ID-metadata-updated" '{orgId:$o, id:$i, input:{title:$t, description:"Sprint 4 demo"}}')" "$ADMIN_USER" "$ORG_ID") || return 1
    updated_title=$(jq -r '.updateMetadata.title' <<<"$updated_metadata")
    assert_equal "$RUN_ID-metadata-updated" "$updated_title" "Metadata update" || return 1

    local found
    found=$(graphql "$search_metadata" "$(jq -nc --arg o "$ORG_ID" --arg q "$RUN_ID-metadata-updated" '{orgId:$o, input:{query:$q}}')" "$ADMIN_USER" "$ORG_ID") || return 1
    local match_count
    match_count=$(jq --arg id "$metadata_id" '[.searchMetadata[] | select(.id == $id)] | length' <<<"$found")
    if [[ "$match_count" -ne 1 ]]; then die "Metadata search did not return the updated item"; return 1; fi

    scenario "FD-05" "Verified Open Images V7 import"
    local dataset_path="$OPEN_IMAGES_DIR/validation-sample.json"
    local database_url="postgresql://asset_user:asset_password@127.0.0.1:5433/asset_db?sslmode=disable"

    local first_text
    first_text=$("$IMPORT_BINARY" -file "$dataset_path" -org-id "$ORG_ID" -user-id "$ADMIN_USER" -database-url "$database_url" 2>&1)
    if [[ $? -ne 0 || "$first_text" != *'"metadata_created": 25'* ]]; then
        die "First real import failed: $first_text"; return 1
    fi
    local second_text
    second_text=$("$IMPORT_BINARY" -file "$dataset_path" -org-id "$ORG_ID" -user-id "$ADMIN_USER" -database-url "$database_url" 2>&1)
    if [[ $? -ne 0 || "$second_text" != *'"metadata_unchanged": 25'* ]]; then
        die "Real import rerun was not idempotent: $second_text"; return 1
    fi
    local before_dry_run
    before_dry_run=$(get_metadata_hash)
    local dry_text
    dry_text=$("$IMPORT_BINARY" -file "$dataset_path" -org-id "$ORG_ID" -user-id "$ADMIN_USER" -database-url "$database_url" -dry-run 2>&1)
    if [[ $? -ne 0 || "$dry_text" != *'"metadata_unchanged": 25'* ]]; then
        die "Real import dry run failed: $dry_text"; return 1
    fi
    assert_equal "$before_dry_run" "$(get_metadata_hash)" "Dry run changed metadata state" || return 1

    local open_images
    open_images=$(graphql "$search_metadata" "$(jq -nc --arg o "$ORG_ID" '{orgId:$o, input:{externalSource:"open_images_v7", limit:25}}')" "$ADMIN_USER" "$ORG_ID") || return 1
    assert_equal "25" "$(jq '.searchMetadata | length' <<<"$open_images")" "GraphQL did not expose 25 imported items" || return 1

    scenario "FD-06" "RBAC and OLP direct grant/revoke"
    local policy_root_data policy_root_id policy_root_path
    policy_root_data=$(graphql "$create_folder" "$(jq -nc --arg o "$ORG_ID" --arg n "$RUN_ID-policy-root" '{orgId:$o, name:$n, parentPath:null}')" "$ADMIN_USER" "$ORG_ID") || return 1
    policy_root_id=$(jq -r '.createFolder.id' <<<"$policy_root_data")
    policy_root_path=$(jq -r '.createFolder.path' <<<"$policy_root_data")

    set_olp 0
    graphql 'query($orgId: ID!, $id: ID!) { folder(orgId: $orgId, id: $id) { id } }' "$(jq -nc --arg o "$ORG_ID" --arg i "$policy_root_id" '{orgId:$o, id:$i}')" "$VIEWER_USER" "$ORG_ID" >/dev/null || return 1
    assert_graphql_error "PM-04" "FORBIDDEN" "$update_folder" "$(jq -nc --arg o "$ORG_ID" --arg i "$policy_root_id" --arg n "$RUN_ID-rbac-denied" '{orgId:$o, id:$i, name:$n}')" "$VIEWER_USER" "$ORG_ID" || return 1
    assert_equal "$RUN_ID-policy-root" "$(invoke_psql "seta-asset-db" "asset_user" "asset_db" "SELECT name FROM folders WHERE id='$policy_root_id';")" "PM-04 deny changed folder" || return 1

    set_olp 1
    assert_graphql_error "PM-05" "FORBIDDEN" "$update_folder" "$(jq -nc --arg o "$ORG_ID" --arg i "$policy_root_id" --arg n "$RUN_ID-olp-denied" '{orgId:$o, id:$i, name:$n}')" "$VIEWER_USER" "$ORG_ID" || return 1
    assert_equal "$RUN_ID-policy-root" "$(invoke_psql "seta-asset-db" "asset_user" "asset_db" "SELECT name FROM folders WHERE id='$policy_root_id';")" "PM-05 deny changed folder" || return 1

    local direct_write
    direct_write=$(grant_permission "folder" "$policy_root_id" "write" "$VIEWER_USER") || return 1
    graphql "$update_folder" "$(jq -nc --arg o "$ORG_ID" --arg i "$policy_root_id" --arg n "$RUN_ID-direct-allowed" '{orgId:$o, id:$i, name:$n}')" "$VIEWER_USER" "$ORG_ID" >/dev/null || return 1
    revoke_permission "$direct_write" || return 1
    assert_graphql_error "PM-08" "FORBIDDEN" "$update_folder" "$(jq -nc --arg o "$ORG_ID" --arg i "$policy_root_id" --arg n "$RUN_ID-revoked" '{orgId:$o, id:$i, name:$n}')" "$VIEWER_USER" "$ORG_ID" || return 1

    scenario "FD-07" "Creator no-bypass, inheritance, and exact manage permission"
    local viewer_created_data viewer_created_id
    viewer_created_data=$(graphql "$create_folder" "$(jq -nc --arg o "$ORG_ID" --arg n "$RUN_ID-viewer-created" --arg p "$policy_root_path" '{orgId:$o, name:$n, parentPath:$p}')" "$ADMIN_USER" "$ORG_ID") || return 1
    viewer_created_id=$(jq -r '.createFolder.id' <<<"$viewer_created_data")
    invoke_psql "seta-asset-db" "asset_user" "asset_db" "UPDATE folders SET created_by='$VIEWER_USER' WHERE id='$viewer_created_id'; SELECT created_by FROM folders WHERE id='$viewer_created_id';" >/dev/null

    set_olp 0
    assert_graphql_error "PM-10-RBAC" "FORBIDDEN" "$update_folder" "$(jq -nc --arg o "$ORG_ID" --arg i "$viewer_created_id" --arg n "$RUN_ID-creator-rbac-bypass" '{orgId:$o, id:$i, name:$n}')" "$VIEWER_USER" "$ORG_ID" || return 1
    set_olp 1
    assert_graphql_error "PM-10" "FORBIDDEN" "$update_folder" "$(jq -nc --arg o "$ORG_ID" --arg i "$viewer_created_id" --arg n "$RUN_ID-creator-bypass" '{orgId:$o, id:$i, name:$n}')" "$VIEWER_USER" "$ORG_ID" || return 1

    local inherited_write
    inherited_write=$(grant_permission "folder" "$policy_root_id" "write" "$VIEWER_USER") || return 1
    graphql "$update_folder" "$(jq -nc --arg o "$ORG_ID" --arg i "$viewer_created_id" --arg n "$RUN_ID-inherited-allowed" '{orgId:$o, id:$i, name:$n}')" "$VIEWER_USER" "$ORG_ID" >/dev/null || return 1

    local exact_manage
    exact_manage=$(grant_permission "folder" "$policy_root_id" "manage_permissions" "$VIEWER_USER") || return 1

    local grant_mutation='mutation($orgId: ID!, $resourceType: ResourceType!, $resourceId: ID!, $action: PermissionAction!, $granteeUserId: ID!) { grantObjectPermission(orgId: $orgId, resourceType: $resourceType, resourceId: $resourceId, action: $action, granteeUserId: $granteeUserId) { id } }'
    local viewer_exact_grant_data viewer_exact_grant
    viewer_exact_grant_data=$(graphql "$grant_mutation" "$(jq -nc --arg o "$ORG_ID" --arg i "$policy_root_id" --arg u "$ADMIN_USER" '{orgId:$o, resourceType:"folder", resourceId:$i, action:"read", granteeUserId:$u}')" "$VIEWER_USER" "$ORG_ID") || return 1
    viewer_exact_grant=$(jq -r '.grantObjectPermission.id' <<<"$viewer_exact_grant_data")
    assert_graphql_error "PM-13" "FORBIDDEN" "$grant_mutation" "$(jq -nc --arg o "$ORG_ID" --arg i "$viewer_created_id" --arg u "$ADMIN_USER" '{orgId:$o, resourceType:"folder", resourceId:$i, action:"read", granteeUserId:$u}')" "$VIEWER_USER" "$ORG_ID" || return 1

    revoke_permission "$viewer_exact_grant" || return 1
    revoke_permission "$exact_manage" || return 1
    revoke_permission "$inherited_write" || return 1

    scenario "FD-08" "Soft delete hides resource and preserves grant"
    local soft_metadata_data soft_metadata_id
    soft_metadata_data=$(graphql "$create_metadata" "$(jq -nc --arg o "$ORG_ID" --arg f "$policy_root_id" --arg t "$RUN_ID-soft-delete" '{orgId:$o, input:{folderId:$f, title:$t, metadataJson:"{}"}}')" "$ADMIN_USER" "$ORG_ID") || return 1
    soft_metadata_id=$(jq -r '.createMetadata.id' <<<"$soft_metadata_data")

    local soft_grant
    soft_grant=$(grant_permission "metadata_item" "$soft_metadata_id" "read" "$VIEWER_USER") || return 1
    local before_grant_count
    before_grant_count=$(invoke_psql "seta-access-db" "access_user" "access_db" "SELECT COUNT(*) FROM access.object_permissions WHERE id='$soft_grant';")

    graphql "$delete_metadata" "$(jq -nc --arg o "$ORG_ID" --arg i "$soft_metadata_id" '{orgId:$o, id:$i}')" "$ADMIN_USER" "$ORG_ID" >/dev/null || return 1

    local after_delete_search
    after_delete_search=$(graphql "$search_metadata" "$(jq -nc --arg o "$ORG_ID" --arg q "$RUN_ID-soft-delete" '{orgId:$o, input:{query:$q}}')" "$ADMIN_USER" "$ORG_ID") || return 1
    assert_equal "0" "$(jq '.searchMetadata | length' <<<"$after_delete_search")" "Soft-deleted metadata remained searchable" || return 1
    assert_equal "$before_grant_count" "$(invoke_psql "seta-access-db" "access_user" "access_db" "SELECT COUNT(*) FROM access.object_permissions WHERE id='$soft_grant';")" "Soft delete removed permission history" || return 1

    scenario "FD-09" "Reset invariants"
    set_olp 0

    scenario "FD-10" "Rehearsal completion"
    printf '\033[32mAll FD-00 through FD-10 assertions passed for %s\033[0m\n' "$RUN_ID"
    DEMO_SUCCEEDED=1
}

run
