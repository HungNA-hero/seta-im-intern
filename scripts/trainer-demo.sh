#!/usr/bin/env bash
#
# Interactive trainer demo script.
# Walks through project functionality: service boundary, folder/metadata lifecycle,
# RBAC vs OLP modes, soft delete, and optional Open Images import.

set -uo pipefail

OPEN_IMAGES_DIR=""
READINESS_TIMEOUT_SECONDS=60
SECTIONS_TO_RUN=()

while [[ $# -gt 0 ]]; do
    case "$1" in
        --open-images-directory) OPEN_IMAGES_DIR="$2"; shift 2 ;;
        --readiness-timeout-seconds) READINESS_TIMEOUT_SECONDS="$2"; shift 2 ;;
        -*) echo "Unknown flag: $1" >&2; exit 1 ;;
        *) SECTIONS_TO_RUN+=("$1"); shift ;;
    esac
done

if [[ -n "$OPEN_IMAGES_DIR" && ! -d "$OPEN_IMAGES_DIR" ]]; then
    echo "Directory $OPEN_IMAGES_DIR does not exist." >&2
    exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
ACCESS_CORE="$REPO_ROOT/services/access-core"
ASSET_CORE="$REPO_ROOT/services/asset-core"

RUN_ID="tdemo-$(date -u +%Y%m%dT%H%M%SZ)-$$"
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

# ---------- helpers ----------

log()   { printf '%s\n' "$*"; }

scenario() {
    local id="$1" title="$2" desc="$3"
    printf '\n\033[36m=== %s: %s ===\033[0m\n' "$id" "$title"
    printf '%s\n\n' "$desc"
    read -r -p "Press Enter to continue" _ || true
    echo
}

show() {
    jq . <<< "$1"
}

check() {
    local expected="$1" actual="$2" label="$3"
    if [[ "$expected" == "$actual" ]]; then
        printf '\033[32m[PASS]\033[0m %s (got: %s)\n' "$label" "$actual"
    else
        printf '\033[33m[WARN]\033[0m %s (expected: %s, actual: %s)\n' "$label" "$expected" "$actual"
    fi
    return 0
}

is_port_open() {
    local port="$1"
    ss -H -ltn "sport = :$port" 2>/dev/null | grep -q .
}

wait_service() {
    local name="$1" url="$2" pid="$3"; shift 3
    local logs=("$@")
    local deadline=$((SECONDS + READINESS_TIMEOUT_SECONDS))
    while (( SECONDS < deadline )); do
        if [[ -n "$pid" ]] && ! kill -0 "$pid" 2>/dev/null; then
            for l in "${logs[@]}"; do [[ -f "$l" ]] && tail -n 100 "$l"; done
            echo "$name exited before readiness" >&2
            return 1
        fi
        if curl -sf -m 1 "$url" -o /dev/null; then
            return 0
        fi
        sleep 0.5
    done
    for l in "${logs[@]}"; do [[ -f "$l" ]] && tail -n 100 "$l"; done
    echo "$name readiness timed out after $READINESS_TIMEOUT_SECONDS seconds" >&2
    return 1
}

cleanup() {
    # Only clean up the script's own temp log dir.
    rm -rf "$TEMP_DIR"
}
trap cleanup EXIT

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

get_graphql_error_code() {
    local query="$1" variables="${2-}" user_id="${3:-}" org_id="${4:-}"
    local response
    response=$(graphql_raw "$query" "$variables" "$user_id" "$org_id")
    jq -r '.errors[0].extensions.code // "SUCCESS"' <<<"$response"
}

invoke_psql() {
    local container="$1" user="$2" database="$3" sql="$4"
    docker exec "$container" psql -U "$user" -d "$database" -Atc "$sql" | tail -n 1 | tr -d '\r'
}

set_olp() {
    local enabled="$1" value
    value=$([[ "$enabled" == "1" ]] && echo true || echo false)
    invoke_psql "seta-access-db" "access_user" "access_db" \
        "UPDATE access.organizations SET olp_enabled=$value WHERE id='$ORG_ID';" >/dev/null
}

grant_permission() {
    local resource_type="$1" resource_id="$2" action="$3" grantee_user="$4" actor="${5:-$ADMIN_USER}"
    local mutation='mutation($orgId: ID!, $resourceType: ResourceType!, $resourceId: ID!, $action: PermissionAction!, $granteeUserId: ID!) { grantObjectPermission(orgId: $orgId, resourceType: $resourceType, resourceId: $resourceId, action: $action, granteeUserId: $granteeUserId) { id } }'
    local vars
    vars=$(jq -nc --arg org "$ORG_ID" --arg rt "$resource_type" --arg rid "$resource_id" --arg act "$action" --arg gu "$grantee_user" \
        '{orgId:$org, resourceType:$rt, resourceId:$rid, action:$act, granteeUserId:$gu}')
    local res
    res=$(graphql_raw "$mutation" "$vars" "$actor" "$ORG_ID")
    jq -r '.data.grantObjectPermission.id' <<<"$res"
}

revoke_permission() {
    local permission_id="$1" actor="${2:-$ADMIN_USER}"
    local mutation='mutation($id: ID!) { revokeObjectPermission(id: $id) }'
    local vars
    vars=$(jq -nc --arg id "$permission_id" '{id:$id}')
    graphql_raw "$mutation" "$vars" "$actor" "$ORG_ID" >/dev/null
}

# ---------- sections ----------

run_architecture() {
    scenario "architecture" "Architecture recap" "This confirms both the Go asset-core and Node access-core services are running and healthy. The Node service acts as the RBAC/OLP boundary, while the Go service owns the actual asset data."
    local go_health node_health
    go_health=$(curl -s "http://127.0.0.1:$GO_PORT/healthz" | jq -r .status)
    check "ok" "$go_health" "Go asset-core /healthz"

    node_health=$(curl -s "http://127.0.0.1:$NODE_PORT/health" | jq -r .status)
    check "ok" "$node_health" "Node access-core /health"
}

run_org_isolation() {
    scenario "org-isolation" "Authentication and organization isolation" "Every request must carry a known user. We'll show that a missing or unknown user is UNAUTHENTICATED, that a user requesting a different org's folder tree is FORBIDDEN, and that none of these denied attempts touch the Asset DB."

    local folder_count_before
    folder_count_before=$(invoke_psql "seta-asset-db" "asset_user" "asset_db" "SELECT COUNT(*) FROM folders;")

    local org_query='query { organizations { id } }'
    local no_user_code
    no_user_code=$(get_graphql_error_code "$org_query" "{}" "" "")
    check "UNAUTHENTICATED" "$no_user_code" "No user header returns UNAUTHENTICATED"

    local unknown_user_code
    unknown_user_code=$(get_graphql_error_code "$org_query" "{}" "$UNKNOWN_USER" "$ORG_ID")
    check "UNAUTHENTICATED" "$unknown_user_code" "Unknown user returns UNAUTHENTICATED"

    local tree_query='query($orgId: ID!) { folderTree(orgId: $orgId) { id name } }'
    local admin_other_org_code
    admin_other_org_code=$(get_graphql_error_code "$tree_query" "$(jq -nc --arg o "$OTHER_ORG_ID" '{orgId:$o}')" "$ADMIN_USER" "$OTHER_ORG_ID")
    check "FORBIDDEN" "$admin_other_org_code" "Admin requesting a different org's folder tree is FORBIDDEN"

    local viewer_other_org_code
    viewer_other_org_code=$(get_graphql_error_code "$tree_query" "$(jq -nc --arg o "$OTHER_ORG_ID" '{orgId:$o}')" "$VIEWER_USER" "$OTHER_ORG_ID")
    check "FORBIDDEN" "$viewer_other_org_code" "Viewer requesting a different org's folder tree is FORBIDDEN"

    local allowed_tree
    allowed_tree=$(graphql_raw "$tree_query" "$(jq -nc --arg o "$ORG_ID" '{orgId:$o}')" "$ADMIN_USER" "$ORG_ID")
    echo "Admin's own org folder tree:"
    show "$allowed_tree"
    local allowed_count
    allowed_count=$(jq '.data.folderTree | length' <<<"$allowed_tree")
    if [[ "$allowed_count" -ge 1 ]]; then
        check "true" "true" "Admin's own org folder tree is non-empty"
    else
        check "true" "false" "Admin's own org folder tree is non-empty"
    fi

    local folder_count_after
    folder_count_after=$(invoke_psql "seta-asset-db" "asset_user" "asset_db" "SELECT COUNT(*) FROM folders;")
    check "$folder_count_before" "$folder_count_after" "Denied cross-org attempts did not change the Asset DB"
}

run_folders() {
    scenario "folders" "Folder tree lifecycle" "We will show the seeded folder tree, create a new folder, rename it, move it, and attempt to delete a folder that still has children."
    
    local tree_query='query($orgId: ID!) { folderTree(orgId: $orgId) { id name } }'
    local tree_res=$(graphql_raw "$tree_query" "$(jq -nc --arg o "$ORG_ID" '{orgId:$o}')" "$ADMIN_USER" "$ORG_ID")
    echo "Initial folder tree:"
    show "$tree_res"
    
    local create_folder='mutation($orgId: ID!, $name: String!, $parentPath: String) { createFolder(orgId: $orgId, name: $name, parentPath: $parentPath) { id name path } }'
    local root_res=$(graphql_raw "$create_folder" "$(jq -nc --arg o "$ORG_ID" --arg n "$RUN_ID-root" '{orgId:$o, name:$n, parentPath:null}')" "$ADMIN_USER" "$ORG_ID")
    echo "Created root folder:"
    show "$root_res"
    local root_id=$(jq -r '.data.createFolder.id' <<<"$root_res")
    local root_path=$(jq -r '.data.createFolder.path' <<<"$root_res")
    
    local child_res=$(graphql_raw "$create_folder" "$(jq -nc --arg o "$ORG_ID" --arg n "$RUN_ID-child" --arg p "$root_path" '{orgId:$o, name:$n, parentPath:$p}')" "$ADMIN_USER" "$ORG_ID")
    echo "Created child folder:"
    show "$child_res"
    local child_id=$(jq -r '.data.createFolder.id' <<<"$child_res")
    local child_path=$(jq -r '.data.createFolder.path' <<<"$child_res")
    
    local delete_folder='mutation($orgId: ID!, $id: ID!) { deleteFolder(orgId: $orgId, id: $id) }'
    local del_err_code=$(get_graphql_error_code "$delete_folder" "$(jq -nc --arg o "$ORG_ID" --arg i "$root_id" '{orgId:$o, id:$i}')" "$ADMIN_USER" "$ORG_ID")
    check "CONFLICT" "$del_err_code" "Delete folder with children returns CONFLICT"
    
    local update_folder='mutation($orgId: ID!, $id: ID!, $name: String) { updateFolder(orgId: $orgId, id: $id, name: $name) { id name path } }'
    local rename_res=$(graphql_raw "$update_folder" "$(jq -nc --arg o "$ORG_ID" --arg i "$child_id" --arg n "$RUN_ID-child-renamed" '{orgId:$o, id:$i, name:$n}')" "$ADMIN_USER" "$ORG_ID")
    echo "Renamed child folder:"
    show "$rename_res"
    local renamed_path=$(jq -r '.data.updateFolder.path' <<<"$rename_res")
    check "$child_path" "$renamed_path" "Rename does not change path"
    
    local move_folder='mutation($orgId: ID!, $id: ID!, $destinationParentId: ID) { moveFolder(orgId: $orgId, id: $id, destinationParentId: $destinationParentId) { id path } }'
    local move_res=$(graphql_raw "$move_folder" "$(jq -nc --arg o "$ORG_ID" --arg i "$child_id" '{orgId:$o, id:$i, destinationParentId:null}')" "$ADMIN_USER" "$ORG_ID")
    echo "Moved child folder to root level:"
    show "$move_res"
}

run_metadata() {
    scenario "metadata" "Metadata lifecycle + search" "We'll create a metadata item under a new folder, update it, and search for it."
    
    local create_folder='mutation($orgId: ID!, $name: String!, $parentPath: String) { createFolder(orgId: $orgId, name: $name, parentPath: $parentPath) { id name path } }'
    local root_res=$(graphql_raw "$create_folder" "$(jq -nc --arg o "$ORG_ID" --arg n "$RUN_ID-meta-root" '{orgId:$o, name:$n, parentPath:null}')" "$ADMIN_USER" "$ORG_ID")
    local root_id=$(jq -r '.data.createFolder.id' <<<"$root_res")
    
    local create_metadata='mutation($orgId: ID!, $input: CreateMetadataInput!) { createMetadata(orgId: $orgId, input: $input) { id title } }'
    local meta_res=$(graphql_raw "$create_metadata" "$(jq -nc --arg o "$ORG_ID" --arg f "$root_id" --arg t "$RUN_ID-metadata" '{orgId:$o, input:{folderId:$f, title:$t, metadataJson:"{\"demo\":true}"}}')" "$ADMIN_USER" "$ORG_ID")
    echo "Created metadata:"
    show "$meta_res"
    local meta_id=$(jq -r '.data.createMetadata.id' <<<"$meta_res")
    
    local update_metadata='mutation($orgId: ID!, $id: ID!, $input: UpdateMetadataInput!) { updateMetadata(orgId: $orgId, id: $id, input: $input) { id title description } }'
    local meta_update_res=$(graphql_raw "$update_metadata" "$(jq -nc --arg o "$ORG_ID" --arg i "$meta_id" --arg t "$RUN_ID-metadata-updated" '{orgId:$o, id:$i, input:{title:$t, description:"Trainer Demo"}}')" "$ADMIN_USER" "$ORG_ID")
    echo "Updated metadata:"
    show "$meta_update_res"
    
    local search_metadata='query($orgId: ID!, $input: MetadataSearchInput!) { searchMetadata(orgId: $orgId, input: $input) { id title externalId } }'
    local search_res=$(graphql_raw "$search_metadata" "$(jq -nc --arg o "$ORG_ID" --arg q "$RUN_ID-metadata-updated" '{orgId:$o, input:{query:$q}}')" "$ADMIN_USER" "$ORG_ID")
    echo "Search results:"
    show "$search_res"
    local found_count=$(jq --arg id "$meta_id" '[.data.searchMetadata[] | select(.id == $id)] | length' <<<"$search_res")
    check "1" "$found_count" "Search returned the updated metadata item"
}

run_rbac() {
    scenario "rbac" "RBAC mode (org seta, olp_enabled = false)" "By default, the org is in RBAC mode. We'll use a viewer account to show that reading succeeds (based on org role) but writing is denied, without even checking object-level grants."
    
    local create_folder='mutation($orgId: ID!, $name: String!, $parentPath: String) { createFolder(orgId: $orgId, name: $name, parentPath: $parentPath) { id name path } }'
    local root_res=$(graphql_raw "$create_folder" "$(jq -nc --arg o "$ORG_ID" --arg n "$RUN_ID-rbac-root" '{orgId:$o, name:$n, parentPath:null}')" "$ADMIN_USER" "$ORG_ID")
    local root_id=$(jq -r '.data.createFolder.id' <<<"$root_res")
    
    local search_metadata='query($orgId: ID!, $input: MetadataSearchInput!) { searchMetadata(orgId: $orgId, input: $input) { id title externalId } }'
    local read_err_code=$(get_graphql_error_code "$search_metadata" "$(jq -nc --arg o "$ORG_ID" --arg q "$RUN_ID" '{orgId:$o, input:{query:$q}}')" "$VIEWER_USER" "$ORG_ID")
    check "SUCCESS" "$read_err_code" "Viewer can read metadata in RBAC mode"
    
    local update_folder='mutation($orgId: ID!, $id: ID!, $name: String) { updateFolder(orgId: $orgId, id: $id, name: $name) { id name path } }'
    local write_err_code=$(get_graphql_error_code "$update_folder" "$(jq -nc --arg o "$ORG_ID" --arg i "$root_id" --arg n "$RUN_ID-rbac-denied" '{orgId:$o, id:$i, name:$n}')" "$VIEWER_USER" "$ORG_ID")
    check "FORBIDDEN" "$write_err_code" "Viewer cannot write in RBAC mode"
}

run_olp() {
    scenario "olp" "Switch to OLP mode" "We flip olp_enabled to true. We'll show how to grant permission directly, how a grant on a parent inherits to a child (but manage_permissions does not), and that a creator does not get implicit access without a grant."
    
    local create_folder='mutation($orgId: ID!, $name: String!, $parentPath: String) { createFolder(orgId: $orgId, name: $name, parentPath: $parentPath) { id name path } }'
    local policy_root_res=$(graphql_raw "$create_folder" "$(jq -nc --arg o "$ORG_ID" --arg n "$RUN_ID-policy-root" '{orgId:$o, name:$n, parentPath:null}')" "$ADMIN_USER" "$ORG_ID")
    local policy_root_id=$(jq -r '.data.createFolder.id' <<<"$policy_root_res")
    local policy_root_path=$(jq -r '.data.createFolder.path' <<<"$policy_root_res")
    
    local policy_child_res=$(graphql_raw "$create_folder" "$(jq -nc --arg o "$ORG_ID" --arg n "$RUN_ID-policy-child" --arg p "$policy_root_path" '{orgId:$o, name:$n, parentPath:$p}')" "$ADMIN_USER" "$ORG_ID")
    local policy_child_id=$(jq -r '.data.createFolder.id' <<<"$policy_child_res")
    
    echo "Enabling OLP mode..."
    set_olp 1
    
    local update_folder='mutation($orgId: ID!, $id: ID!, $name: String) { updateFolder(orgId: $orgId, id: $id, name: $name) { id name path } }'
    
    local olp_write_err_code=$(get_graphql_error_code "$update_folder" "$(jq -nc --arg o "$ORG_ID" --arg i "$policy_root_id" --arg n "$RUN_ID-olp-denied" '{orgId:$o, id:$i, name:$n}')" "$VIEWER_USER" "$ORG_ID")
    check "FORBIDDEN" "$olp_write_err_code" "Viewer cannot write in OLP mode without grant"
    
    local grant_id=$(grant_permission "folder" "$policy_root_id" "write" "$VIEWER_USER")
    echo "Granted write permission directly to viewer (grant ID: $grant_id)"
    
    local olp_write_granted_err_code=$(get_graphql_error_code "$update_folder" "$(jq -nc --arg o "$ORG_ID" --arg i "$policy_root_id" --arg n "$RUN_ID-direct-allowed" '{orgId:$o, id:$i, name:$n}')" "$VIEWER_USER" "$ORG_ID")
    check "SUCCESS" "$olp_write_granted_err_code" "Viewer can write after direct grant"
    
    revoke_permission "$grant_id"
    echo "Revoked direct write permission"
    
    local olp_write_revoked_err_code=$(get_graphql_error_code "$update_folder" "$(jq -nc --arg o "$ORG_ID" --arg i "$policy_root_id" --arg n "$RUN_ID-revoked-denied" '{orgId:$o, id:$i, name:$n}')" "$VIEWER_USER" "$ORG_ID")
    check "FORBIDDEN" "$olp_write_revoked_err_code" "Viewer cannot write after revoke"
    
    echo -e "\nTesting inheritance..."
    local inherited_grant_id=$(grant_permission "folder" "$policy_root_id" "write" "$VIEWER_USER")
    local child_write_err_code=$(get_graphql_error_code "$update_folder" "$(jq -nc --arg o "$ORG_ID" --arg i "$policy_child_id" --arg n "$RUN_ID-inherited-allowed" '{orgId:$o, id:$i, name:$n}')" "$VIEWER_USER" "$ORG_ID")
    check "SUCCESS" "$child_write_err_code" "Viewer can write to child folder via inherited grant"
    revoke_permission "$inherited_grant_id"
    
    local manage_grant_id=$(grant_permission "folder" "$policy_root_id" "manage_permissions" "$VIEWER_USER")
    local grant_mutation='mutation($orgId: ID!, $resourceType: ResourceType!, $resourceId: ID!, $action: PermissionAction!, $granteeUserId: ID!) { grantObjectPermission(orgId: $orgId, resourceType: $resourceType, resourceId: $resourceId, action: $action, granteeUserId: $granteeUserId) { id } }'

    local exact_grant_res=$(graphql_raw "$grant_mutation" "$(jq -nc --arg o "$ORG_ID" --arg i "$policy_root_id" --arg u "$ADMIN_USER" '{orgId:$o, resourceType:"folder", resourceId:$i, action:"read", granteeUserId:$u}')" "$VIEWER_USER" "$ORG_ID")
    local exact_grant_id=$(jq -r '.data.grantObjectPermission.id' <<<"$exact_grant_res")
    if [[ "$exact_grant_id" != "null" && -n "$exact_grant_id" ]]; then
        check "true" "true" "Viewer with exact manage_permissions can grant on the exact resource"
    else
        check "true" "false" "Viewer with exact manage_permissions can grant on the exact resource"
    fi

    local child_grant_err_code=$(get_graphql_error_code "$grant_mutation" "$(jq -nc --arg o "$ORG_ID" --arg i "$policy_child_id" --arg u "$ADMIN_USER" '{orgId:$o, resourceType:"folder", resourceId:$i, action:"read", granteeUserId:$u}')" "$VIEWER_USER" "$ORG_ID")
    check "FORBIDDEN" "$child_grant_err_code" "Manage permissions does not inherit to child"

    [[ "$exact_grant_id" != "null" && -n "$exact_grant_id" ]] && revoke_permission "$exact_grant_id"
    revoke_permission "$manage_grant_id"
    
    echo -e "\nTesting creator implicit access..."
    local viewer_created_res=$(graphql_raw "$create_folder" "$(jq -nc --arg o "$ORG_ID" --arg n "$RUN_ID-viewer-created" --arg p "$policy_root_path" '{orgId:$o, name:$n, parentPath:$p}')" "$ADMIN_USER" "$ORG_ID")
    local viewer_created_id=$(jq -r '.data.createFolder.id' <<<"$viewer_created_res")
    invoke_psql "seta-asset-db" "asset_user" "asset_db" "UPDATE folders SET created_by='$VIEWER_USER' WHERE id='$viewer_created_id';" >/dev/null
    
    local creator_bypass_err_code=$(get_graphql_error_code "$update_folder" "$(jq -nc --arg o "$ORG_ID" --arg i "$viewer_created_id" --arg n "$RUN_ID-creator-bypass" '{orgId:$o, id:$i, name:$n}')" "$VIEWER_USER" "$ORG_ID")
    check "FORBIDDEN" "$creator_bypass_err_code" "Creator has no implicit access without a grant"
    
    set_olp 0
    echo -e "\nOLP mode disabled (restored to RBAC)."
}

run_soft_delete() {
    scenario "soft-delete" "Soft delete keeps grants" "When a resource is deleted, it disappears from searches, but its grants remain in the access DB."
    
    local create_folder='mutation($orgId: ID!, $name: String!, $parentPath: String) { createFolder(orgId: $orgId, name: $name, parentPath: $parentPath) { id name path } }'
    local root_res=$(graphql_raw "$create_folder" "$(jq -nc --arg o "$ORG_ID" --arg n "$RUN_ID-soft-root" '{orgId:$o, name:$n, parentPath:null}')" "$ADMIN_USER" "$ORG_ID")
    local root_id=$(jq -r '.data.createFolder.id' <<<"$root_res")
    
    local create_metadata='mutation($orgId: ID!, $input: CreateMetadataInput!) { createMetadata(orgId: $orgId, input: $input) { id title } }'
    local soft_del_res=$(graphql_raw "$create_metadata" "$(jq -nc --arg o "$ORG_ID" --arg f "$root_id" --arg t "$RUN_ID-soft-delete" '{orgId:$o, input:{folderId:$f, title:$t, metadataJson:"{}"}}')" "$ADMIN_USER" "$ORG_ID")
    local soft_del_id=$(jq -r '.data.createMetadata.id' <<<"$soft_del_res")
    
    local soft_del_grant_id=$(grant_permission "metadata_item" "$soft_del_id" "read" "$VIEWER_USER")
    
    local delete_metadata='mutation($orgId: ID!, $id: ID!) { deleteMetadata(orgId: $orgId, id: $id) }'
    graphql_raw "$delete_metadata" "$(jq -nc --arg o "$ORG_ID" --arg i "$soft_del_id" '{orgId:$o, id:$i}')" "$ADMIN_USER" "$ORG_ID" >/dev/null
    
    local search_metadata='query($orgId: ID!, $input: MetadataSearchInput!) { searchMetadata(orgId: $orgId, input: $input) { id title externalId } }'
    local del_search_res=$(graphql_raw "$search_metadata" "$(jq -nc --arg o "$ORG_ID" --arg q "$RUN_ID-soft-delete" '{orgId:$o, input:{query:$q}}')" "$ADMIN_USER" "$ORG_ID")
    local del_search_count=$(jq '.data.searchMetadata | length' <<<"$del_search_res")
    check "0" "$del_search_count" "Deleted item disappears from search"
    
    local grant_count=$(invoke_psql "seta-access-db" "access_user" "access_db" "SELECT COUNT(*) FROM access.object_permissions WHERE id='$soft_del_grant_id';")
    check "1" "$grant_count" "Grant remains in access_db after soft delete"
}

run_open_images() {
    scenario "open-images" "Open Images import" "Imports a verified set of Open Images dataset and demonstrates idempotency."
    if [[ -z "$OPEN_IMAGES_DIR" ]]; then
        echo "No --open-images-directory flag provided. Skipping Open Images import."
        return 0
    fi
    
    local dataset_path="$OPEN_IMAGES_DIR/validation-sample.json"
    local database_url="postgresql://asset_user:asset_password@127.0.0.1:5433/asset_db?sslmode=disable"
    
    echo "Running fetch_open_images_metadata.sh --verify-only..."
    "$SCRIPT_DIR/fetch_open_images_metadata.sh" --verify-only --output-dir "$OPEN_IMAGES_DIR" || {
        echo "Failed to verify open images fixture" >&2
        return 1
    }
    
    echo "First import run:"
    local first_output=$("$IMPORT_BINARY" -file "$dataset_path" -org-id "$ORG_ID" -user-id "$ADMIN_USER" -database-url "$database_url" 2>&1)
    echo "$first_output"
    if [[ "$first_output" == *'"metadata_created": 25'* ]]; then
        check "true" "true" "First import created 25 items"
    else
        check "true" "false" "First import created 25 items"
    fi
    
    echo "Second import run (idempotent):"
    local second_output=$("$IMPORT_BINARY" -file "$dataset_path" -org-id "$ORG_ID" -user-id "$ADMIN_USER" -database-url "$database_url" 2>&1)
    echo "$second_output"
    if [[ "$second_output" == *'"metadata_unchanged": 25'* ]]; then
        check "true" "true" "Second import left 25 items unchanged"
    else
        check "true" "false" "Second import left 25 items unchanged"
    fi
    
    local search_metadata='query($orgId: ID!, $input: MetadataSearchInput!) { searchMetadata(orgId: $orgId, input: $input) { id title externalId } }'
    local open_images_res=$(graphql_raw "$search_metadata" "$(jq -nc --arg o "$ORG_ID" '{orgId:$o, input:{externalSource:"open_images_v7", limit:25}}')" "$ADMIN_USER" "$ORG_ID")
    local open_images_count=$(jq '.data.searchMetadata | length' <<<"$open_images_res")
    check "25" "$open_images_count" "Queryable imported items"
}

run_section() {
    case "$1" in
        architecture) run_architecture ;;
        org-isolation) run_org_isolation ;;
        folders) run_folders ;;
        metadata) run_metadata ;;
        rbac) run_rbac ;;
        olp) run_olp ;;
        soft-delete) run_soft_delete ;;
        open-images) run_open_images ;;
        *) echo "Unknown section: $1" >&2 ;;
    esac
}

show_menu() {
    echo ""
    echo "=== Trainer Demo Menu ==="
    echo "1) architecture   - Service boundary recap, health checks"
    echo "2) org-isolation  - Unauthenticated/unknown user and cross-org access are denied"
    echo "3) folders        - Seeded tree, create/rename/move, delete-with-children CONFLICT"
    echo "4) metadata       - Create/update/search a metadata item"
    echo "5) rbac           - Viewer read-ok / write-FORBIDDEN under default RBAC mode"
    echo "6) olp            - OLP direct grant/revoke, inheritance, manage exactness, creator-no-bypass"
    echo "7) soft-delete    - Delete a metadata item, show its grant row survives"
    echo "8) open-images    - Open Images import (requires --open-images-directory)"
    echo "0) quit         - Exit the demo"
    echo "all)            - Run all sections sequentially"
    echo ""
}

boot_services() {
    mkdir -p "$TEMP_DIR"
    cd "$REPO_ROOT"

    if is_port_open "$GO_PORT" && is_port_open "$NODE_PORT"; then
        echo "Services on ports $GO_PORT and $NODE_PORT are already responding. Skipping boot."
    else
        echo "Services not running. Booting environment..."
        npm run docker:up || exit 1
        npm run docker:migrate || exit 1
        
        npm --prefix services/access-core run build || exit 1
        (cd "$ASSET_CORE" && go build -o "$GO_BINARY" ./cmd/server/main.go) || exit 1

        ASSET_DB_HOST=127.0.0.1 ASSET_DB_PORT=5433 ASSET_DB_NAME=asset_db ASSET_DB_USER=asset_user \
            ASSET_DB_PASSWORD=asset_password PORT="$GO_PORT" \
            "$GO_BINARY" >"$GO_STDOUT" 2>"$GO_STDERR" &
        GO_PID=$!
        wait_service "Go Asset Core" "http://127.0.0.1:$GO_PORT/healthz" "$GO_PID" "$GO_STDOUT" "$GO_STDERR" || exit 1

        ACCESS_DB_HOST=127.0.0.1 ACCESS_DB_PORT=5434 ACCESS_DB_NAME=access_db ACCESS_DB_USER=access_user \
            ACCESS_DB_PASSWORD=access_password DATABASE_URL="postgresql://access_user:access_password@127.0.0.1:5434/access_db" \
            GO_ASSET_URL="http://127.0.0.1:$GO_PORT" PORT="$NODE_PORT" \
            node "$ACCESS_CORE/dist/index.js" >"$NODE_STDOUT" 2>"$NODE_STDERR" &
        NODE_PID=$!
        wait_service "Node Access Core" "http://127.0.0.1:$NODE_PORT/health" "$NODE_PID" "$NODE_STDOUT" "$NODE_STDERR" || exit 1
    fi

    if [[ -n "$OPEN_IMAGES_DIR" ]]; then
        if [[ ! -f "$IMPORT_BINARY" ]]; then
            echo "Building import CLI..."
            (cd "$ASSET_CORE" && go build -o "$IMPORT_BINARY" ./cmd/import-sample/main.go) || exit 1
        fi
    fi
}

# Run Boot
boot_services

ALL_SECTIONS=(architecture org-isolation folders metadata rbac olp soft-delete open-images)

if [[ ${#SECTIONS_TO_RUN[@]} -gt 0 ]]; then
    if [[ "${SECTIONS_TO_RUN[0]}" == "all" ]]; then
        for s in "${ALL_SECTIONS[@]}"; do
            run_section "$s"
        done
    else
        for s in "${SECTIONS_TO_RUN[@]}"; do
            run_section "$s"
        done
    fi
else
    while true; do
        show_menu
        read -r -p "Select a section to run: " choice
        case "$choice" in
            1|architecture) run_section "architecture" ;;
            2|org-isolation) run_section "org-isolation" ;;
            3|folders) run_section "folders" ;;
            4|metadata) run_section "metadata" ;;
            5|rbac) run_section "rbac" ;;
            6|olp) run_section "olp" ;;
            7|soft-delete) run_section "soft-delete" ;;
            8|open-images) run_section "open-images" ;;
            all)
                for s in "${ALL_SECTIONS[@]}"; do
                    run_section "$s"
                done
                ;;
            0|quit|q|exit)
                echo "Exiting..."
                break
                ;;
            *)
                echo "Invalid choice: $choice"
                ;;
        esac
    done
fi

echo -e "\nGraphiQL URL: http://localhost:4000/graphql"
echo "You can keep poking at the endpoints."
