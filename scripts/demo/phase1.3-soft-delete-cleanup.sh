#!/usr/bin/env bash

PHASE13_LAST_FOLDER_ID=""
PHASE13_LAST_FOLDER_PATH=""
PHASE13_LAST_FOLDER_DELETE_JOB=""
PHASE13_LAST_FOLDER_UNIT=""
PHASE13_LAST_RESTORE_JOB=""

phase13_create_named_folder() {
    local area="$1" step="$2" name="$3" parent_path="${4:-}"
    phase13_graphql "$area" "$step" \
      'mutation($orgId:ID!,$parentPath:String,$name:String!){createFolder(orgId:$orgId,parentPath:$parentPath,name:$name){id name path}}' \
      "$(jq -cn --arg orgId "$ORG_ID" --arg parentPath "$parent_path" --arg name "$name" \
        '{orgId:$orgId,parentPath:(if $parentPath=="" then null else $parentPath end),name:$name}')"
    PHASE13_LAST_FOLDER_ID="$(jq -r '.data.createFolder.id' <<<"$PHASE13_LAST_JSON")"
    PHASE13_LAST_FOLDER_PATH="$(jq -r '.data.createFolder.path' <<<"$PHASE13_LAST_JSON")"
    [[ "$PHASE13_LAST_FOLDER_ID" =~ ^[0-9a-f-]{36}$ && -n "$PHASE13_LAST_FOLDER_PATH" ]] \
      || phase13_die "$step returned an invalid folder"
}

phase13_create_metadata_in_folder() {
    local area="$1" step="$2" folder_id="$3" title="$4"
    phase13_graphql "$area" "$step" \
      'mutation($orgId:ID!,$input:CreateMetadataInput!){createMetadata(orgId:$orgId,input:$input){id folderId title}}' \
      "$(jq -cn --arg orgId "$ORG_ID" --arg folderId "$folder_id" --arg title "$title" \
        '{orgId:$orgId,input:{folderId:$folderId,title:$title,labels:["phase1.3-demo"],metadataJson:"{}"}}')"
    jq -r '.data.createMetadata.id' <<<"$PHASE13_LAST_JSON"
}

phase13_delete_folder_tree() {
    local area="$1" step_prefix="$2" folder_id="$3" preview_id confirmation_token
    phase13_graphql "$area" "$step_prefix-preview" \
      'mutation($orgId:ID!,$folderId:ID!){previewFolderDeletion(orgId:$orgId,folderId:$folderId){id rootFolderId activeFolderCount activeMetadataCount tombstoneFolderCount tombstoneMetadataCount totalRows confirmationToken expiresAt}}' \
      "$(jq -cn --arg orgId "$ORG_ID" --arg folderId "$folder_id" '{orgId:$orgId,folderId:$folderId}')"
    preview_id="$(jq -r '.data.previewFolderDeletion.id' <<<"$PHASE13_LAST_JSON")"
    confirmation_token="$(jq -r '.data.previewFolderDeletion.confirmationToken' <<<"$PHASE13_LAST_JSON")"
    [[ "$preview_id" =~ ^[0-9a-f-]{36}$ && -n "$confirmation_token" && "$confirmation_token" != "null" ]] \
      || phase13_die "$step_prefix did not return a usable deletion preview"
    phase13_graphql "$area" "$step_prefix-confirm" \
      'mutation($orgId:ID!,$folderId:ID!,$previewId:ID!,$token:String!){confirmFolderDeletion(orgId:$orgId,folderId:$folderId,previewId:$previewId,confirmationToken:$token){id rootFolderId status activeFolderCount activeMetadataCount tombstoneFolderCount tombstoneMetadataCount}}' \
      "$(jq -cn --arg orgId "$ORG_ID" --arg folderId "$folder_id" --arg previewId "$preview_id" --arg token "$confirmation_token" \
        '{orgId:$orgId,folderId:$folderId,previewId:$previewId,token:$token}')"
    PHASE13_LAST_FOLDER_DELETE_JOB="$(jq -r '.data.confirmFolderDeletion.id' <<<"$PHASE13_LAST_JSON")"
    [[ "$PHASE13_LAST_FOLDER_DELETE_JOB" =~ ^[0-9a-f-]{36}$ ]] || phase13_die "$step_prefix did not queue a deletion job"
    phase13_wait_asset_sql "folder deletion job $PHASE13_LAST_FOLDER_DELETE_JOB" \
      "SELECT status FROM folder_deletion_jobs WHERE id='$PHASE13_LAST_FOLDER_DELETE_JOB'::uuid;" succeeded 120 >/dev/null
    PHASE13_LAST_FOLDER_UNIT="$(phase13_asset_psql "SELECT id FROM asset_lifecycle_units WHERE org_id='$ORG_ID'::uuid AND root_resource_type='FOLDER' AND root_resource_id='$folder_id'::uuid AND state='DELETED' ORDER BY created_at DESC LIMIT 1;")"
    [[ "$PHASE13_LAST_FOLDER_UNIT" =~ ^[0-9a-f-]{36}$ ]] || phase13_die "$step_prefix did not create a durable lifecycle unit"
}

phase13_restore_unit_and_wait() {
    local area="$1" step="$2" unit_id="$3"
    phase13_graphql "$area" "$step" \
      'mutation($orgId:ID!,$unitId:ID!){restoreLifecycleUnit(orgId:$orgId,unitId:$unitId){jobId lifecycleUnitId operation status attempts failureCode}}' \
      "$(jq -cn --arg orgId "$ORG_ID" --arg unitId "$unit_id" '{orgId:$orgId,unitId:$unitId}')"
    PHASE13_LAST_RESTORE_JOB="$(jq -r '.data.restoreLifecycleUnit.jobId' <<<"$PHASE13_LAST_JSON")"
    [[ "$PHASE13_LAST_RESTORE_JOB" =~ ^[0-9a-f-]{36}$ ]] || phase13_die "$step did not queue a restore job"
    phase13_wait_asset_sql "restore job $PHASE13_LAST_RESTORE_JOB" \
      "SELECT status FROM asset_lifecycle_jobs WHERE id='$PHASE13_LAST_RESTORE_JOB'::uuid;" SUCCEEDED 120 >/dev/null
}

phase13_run_soft_delete_cleanup() {
    local asset_id first_unit second_unit restore_job cleanup_run purge_job
    local unrelated_eligible active_runs baseline_other_units baseline_other_metadata after_other_units after_other_metadata
    local tree_a tree_a_path tree_b tree_c tree_d tree_unit tree_delete_job tree_collision
    local nested_a nested_a_path nested_b nested_c nested_d nested_a_unit nested_b_unit
    local collision_source collision_active collision_unit restored_title
    local permission_asset permission_unit no_access_user viewer_user
    local cursor_id cursor_unit cursor_first_id cursor_first_unit cursor_second_id cursor_end
    local -a cursor_ids cursor_units

    phase13_pause "1.1 Create one run-owned metadata item through GraphQL"
    phase13_create_demo_folder soft-delete
    asset_id="$(phase13_create_metadata soft-delete "$RUN_ID soft-delete cleanup")"
    [[ "$asset_id" =~ ^[0-9a-f-]{36}$ ]] || phase13_die "Metadata creation returned an invalid ID"
    phase13_log "Created run-owned asset: $asset_id"
    baseline_other_units="$(phase13_asset_psql "SELECT count(*) FROM asset_lifecycle_units WHERE root_resource_id <> '$asset_id'::uuid;")"
    baseline_other_metadata="$(phase13_asset_psql "SELECT count(*) FROM metadata_items WHERE id <> '$asset_id'::uuid;")"
    phase13_record_asset_query soft-delete created \
      "SELECT json_build_object('assetId',m.id,'title',m.title,'deletedAt',m.deleted_at,'lifecycleUnitId',m.lifecycle_unit_id) FROM metadata_items m WHERE m.id='$asset_id'::uuid;" >/dev/null
    phase13_case_pass

    phase13_pause "1.2 Soft-delete the item and prove normal reads hide it"
    phase13_graphql soft-delete delete-metadata \
      'mutation($orgId:ID!,$id:ID!){deleteMetadata(orgId:$orgId,id:$id)}' \
      "$(jq -cn --arg orgId "$ORG_ID" --arg id "$asset_id" '{orgId:$orgId,id:$id}')"
    [[ "$(jq -r '.data.deleteMetadata' <<<"$PHASE13_LAST_JSON")" == "true" ]] || phase13_die "deleteMetadata did not return true"
    phase13_graphql soft-delete normal-read-after-delete \
      'query($orgId:ID!,$id:ID!){metadataItem(orgId:$orgId,id:$id){id title}}' \
      "$(jq -cn --arg orgId "$ORG_ID" --arg id "$asset_id" '{orgId:$orgId,id:$id}')"
    [[ "$(jq -r '.data.metadataItem' <<<"$PHASE13_LAST_JSON")" == "null" ]] || phase13_die "Deleted metadata remained visible through the normal read"
    first_unit="$(phase13_asset_psql "SELECT id FROM asset_lifecycle_units WHERE org_id='$ORG_ID'::uuid AND root_resource_type='METADATA' AND root_resource_id='$asset_id'::uuid AND state='DELETED';")"
    [[ "$first_unit" =~ ^[0-9a-f-]{36}$ ]] || phase13_die "First deleted lifecycle unit was not durable"
    phase13_case_pass

    phase13_pause "1.3 Show the exact item in Recycle Bin"
    phase13_graphql soft-delete recycle-bin-after-delete \
      'query($orgId:ID!){recycleBin(orgId:$orgId,input:{first:100}){nodes{lifecycleUnitId resourceType resourceId displayName deletedAt}pageInfo{hasNextPage}}}' \
      "$(jq -cn --arg orgId "$ORG_ID" '{orgId:$orgId}')"
    [[ "$(jq --arg id "$asset_id" '[.data.recycleBin.nodes[]|select(.resourceId==$id and .resourceType=="METADATA")]|length' <<<"$PHASE13_LAST_JSON")" == "1" ]] \
      || phase13_die "Recycle Bin did not contain exactly one entry for the deleted item"
    phase13_record_asset_query soft-delete first-delete-state \
      "SELECT json_build_object('unitId',u.id,'state',u.state,'retentionUntil',u.retention_until,'assetDeletedAt',m.deleted_at) FROM asset_lifecycle_units u JOIN metadata_items m ON m.id=u.root_resource_id WHERE u.id='$first_unit'::uuid;" >/dev/null
    phase13_case_pass

    phase13_pause "1.4 Restore the lifecycle unit and wait for the worker-owned job"
    phase13_graphql soft-delete restore-lifecycle-unit \
      'mutation($orgId:ID!,$unitId:ID!){restoreLifecycleUnit(orgId:$orgId,unitId:$unitId){jobId lifecycleUnitId operation status attempts failureCode}}' \
      "$(jq -cn --arg orgId "$ORG_ID" --arg unitId "$first_unit" '{orgId:$orgId,unitId:$unitId}')"
    restore_job="$(jq -r '.data.restoreLifecycleUnit.jobId' <<<"$PHASE13_LAST_JSON")"
    phase13_wait_asset_sql "restore job $restore_job" \
      "SELECT status FROM asset_lifecycle_jobs WHERE id='$restore_job'::uuid;" SUCCEEDED 90 >/dev/null
    phase13_graphql soft-delete restore-job-succeeded \
      'query($orgId:ID!,$jobId:ID!){lifecycleJob(orgId:$orgId,jobId:$jobId){jobId lifecycleUnitId operation status attempts failureCode completedAt}}' \
      "$(jq -cn --arg orgId "$ORG_ID" --arg jobId "$restore_job" '{orgId:$orgId,jobId:$jobId}')"
    [[ "$(jq -r '.data.lifecycleJob.status' <<<"$PHASE13_LAST_JSON")" == "SUCCEEDED" ]] || phase13_die "Restore job did not succeed"
    phase13_graphql soft-delete normal-read-after-restore \
      'query($orgId:ID!,$id:ID!){metadataItem(orgId:$orgId,id:$id){id title}}' \
      "$(jq -cn --arg orgId "$ORG_ID" --arg id "$asset_id" '{orgId:$orgId,id:$id}')"
    [[ "$(jq -r '.data.metadataItem.id' <<<"$PHASE13_LAST_JSON")" == "$asset_id" ]] || phase13_die "Restored metadata is not visible"
    phase13_case_pass

    phase13_pause "1.5 Delete it again to create a fresh retention unit"
    phase13_graphql soft-delete delete-metadata-again \
      'mutation($orgId:ID!,$id:ID!){deleteMetadata(orgId:$orgId,id:$id)}' \
      "$(jq -cn --arg orgId "$ORG_ID" --arg id "$asset_id" '{orgId:$orgId,id:$id}')"
    second_unit="$(phase13_asset_psql "SELECT id FROM asset_lifecycle_units WHERE org_id='$ORG_ID'::uuid AND root_resource_id='$asset_id'::uuid AND state='DELETED' ORDER BY created_at DESC LIMIT 1;")"
    [[ "$second_unit" =~ ^[0-9a-f-]{36}$ && "$second_unit" != "$first_unit" ]] || phase13_die "Second delete did not create a fresh lifecycle unit"
    phase13_case_pass

    phase13_pause "1.6 Safety gate: prove no unrelated work can be swept"
    unrelated_eligible="$(phase13_asset_psql "SELECT count(*) FROM asset_lifecycle_units WHERE state='DELETED' AND retention_until IS NOT NULL AND retention_until <= statement_timestamp() AND id <> '$second_unit'::uuid;")"
    active_runs="$(phase13_asset_psql "SELECT count(*) FROM asset_lifecycle_cleanup_runs WHERE status IN ('QUEUED','RUNNING');")"
    [[ "$unrelated_eligible" == "0" ]] || phase13_die "Refusing cleanup: $unrelated_eligible unrelated retention-eligible lifecycle unit(s) exist"
    [[ "$active_runs" == "0" ]] || phase13_die "Refusing cleanup: $active_runs pre-existing cleanup run(s) are active"
    phase13_log "Safety gate passed: zero unrelated eligible units and zero active cleanup runs."
    phase13_case_pass

    phase13_pause "1.7 Demo time travel: expire only the exact run-owned unit"
    phase13_asset_psql "UPDATE asset_lifecycle_units SET retention_until=statement_timestamp()-interval '1 second' WHERE id='$second_unit'::uuid AND org_id='$ORG_ID'::uuid AND root_resource_id='$asset_id'::uuid AND state='DELETED';" >/dev/null
    [[ "$(phase13_asset_psql "SELECT count(*) FROM asset_lifecycle_units WHERE state='DELETED' AND retention_until <= statement_timestamp();")" == "1" ]] \
      || phase13_die "Post-time-travel eligibility was not exactly one unit"
    phase13_record_asset_query soft-delete retention-expired \
      "SELECT json_build_object('unitId',id,'state',state,'retentionUntil',retention_until) FROM asset_lifecycle_units WHERE id='$second_unit'::uuid;" >/dev/null
    phase13_log "Demo-only setup backdated exactly lifecycle unit $second_unit."
    phase13_case_pass

    phase13_pause "1.8 Create one durable cleanup run; the production worker performs PURGE"
    cleanup_run="$(phase13_asset_psql "WITH lease AS (INSERT INTO asset_lifecycle_scheduler_leases(scheduler_name) VALUES ('$RUN_ID') RETURNING scheduler_name), created_run AS (INSERT INTO asset_lifecycle_cleanup_runs(scheduler_name,run_date,timezone,status) SELECT scheduler_name,CURRENT_DATE,'UTC','QUEUED' FROM lease RETURNING id) SELECT id FROM created_run;")"
    [[ "$cleanup_run" =~ ^[0-9a-f-]{36}$ ]] || phase13_die "Cleanup run was not created"
    phase13_log "Created one isolated durable cleanup run $cleanup_run for the production worker demo."
    phase13_wait_asset_sql "cleanup run $cleanup_run" \
      "SELECT status FROM asset_lifecycle_cleanup_runs WHERE id='$cleanup_run'::uuid;" SUCCEEDED 120 >/dev/null
    phase13_wait_asset_sql "purge of lifecycle unit $second_unit" \
      "SELECT state FROM asset_lifecycle_units WHERE id='$second_unit'::uuid;" PURGED 120 >/dev/null
    purge_job="$(phase13_asset_psql "SELECT id FROM asset_lifecycle_jobs WHERE unit_id='$second_unit'::uuid AND operation='PURGE' ORDER BY created_at DESC LIMIT 1;")"
    [[ "$(phase13_asset_psql "SELECT status FROM asset_lifecycle_jobs WHERE id='$purge_job'::uuid;")" == "SUCCEEDED" ]] || phase13_die "Purge job did not succeed"
    phase13_case_pass

    phase13_pause "1.9 Verify the run-owned asset was purged and unrelated rows were unchanged"
    [[ "$(phase13_asset_psql "SELECT count(*) FROM metadata_items WHERE id='$asset_id'::uuid;")" == "0" ]] || phase13_die "Purged metadata row still exists"
    after_other_units="$(phase13_asset_psql "SELECT count(*) FROM asset_lifecycle_units WHERE root_resource_id <> '$asset_id'::uuid;")"
    after_other_metadata="$(phase13_asset_psql "SELECT count(*) FROM metadata_items WHERE id <> '$asset_id'::uuid;")"
    [[ "$after_other_units" == "$baseline_other_units" ]] || phase13_die "Unrelated lifecycle-unit count changed"
    [[ "$after_other_metadata" == "$baseline_other_metadata" ]] || phase13_die "Unrelated metadata count changed"
    phase13_record_asset_query soft-delete cleanup-complete \
      "SELECT json_build_object('cleanupRun',(SELECT status FROM asset_lifecycle_cleanup_runs WHERE id='$cleanup_run'::uuid),'unitState',(SELECT state FROM asset_lifecycle_units WHERE id='$second_unit'::uuid),'purgeJob',(SELECT status FROM asset_lifecycle_jobs WHERE id='$purge_job'::uuid),'assetRows',(SELECT count(*) FROM metadata_items WHERE id='$asset_id'::uuid));" >/dev/null
    phase13_case_pass

    phase13_pause "1.10 Delete a folder tree asynchronously through preview and confirmation"
    phase13_create_named_folder soft-delete tree-create-a "$RUN_ID tree A"
    tree_a="$PHASE13_LAST_FOLDER_ID"
    tree_a_path="$PHASE13_LAST_FOLDER_PATH"
    phase13_create_named_folder soft-delete tree-create-b "$RUN_ID tree B" "$tree_a_path"
    tree_b="$PHASE13_LAST_FOLDER_ID"
    phase13_create_named_folder soft-delete tree-create-c "$RUN_ID tree C" "$tree_a_path"
    tree_c="$PHASE13_LAST_FOLDER_ID"
    tree_d="$(phase13_create_metadata_in_folder soft-delete tree-create-d "$tree_c" "$RUN_ID tree D")"
    phase13_delete_folder_tree soft-delete tree-delete "$tree_a"
    tree_unit="$PHASE13_LAST_FOLDER_UNIT"
    tree_delete_job="$PHASE13_LAST_FOLDER_DELETE_JOB"
    [[ "$(phase13_asset_psql "SELECT count(*) FROM folders WHERE id IN ('$tree_a'::uuid,'$tree_b'::uuid,'$tree_c'::uuid) AND deleted_at IS NOT NULL AND lifecycle_unit_id='$tree_unit'::uuid;")" == "3" ]] \
      || phase13_die "Folder tree members were not tombstoned under one lifecycle unit"
    [[ "$(phase13_asset_psql "SELECT count(*) FROM metadata_items WHERE id='$tree_d'::uuid AND deleted_at IS NOT NULL AND lifecycle_unit_id='$tree_unit'::uuid;")" == "1" ]] \
      || phase13_die "Folder tree metadata was not tombstoned under the root lifecycle unit"
    phase13_record_asset_query soft-delete tree-delete-state \
      "SELECT json_build_object('deleteJob',(SELECT status FROM folder_deletion_jobs WHERE id='$tree_delete_job'::uuid),'unit',u.id,'unitState',u.state,'folders',(SELECT count(*) FROM folders WHERE lifecycle_unit_id=u.id),'metadata',(SELECT count(*) FROM metadata_items WHERE lifecycle_unit_id=u.id)) FROM asset_lifecycle_units u WHERE u.id='$tree_unit'::uuid;" >/dev/null
    phase13_case_pass

    phase13_pause "1.11 Prove Recycle Bin is root-only and restore resolves a folder-name collision"
    phase13_graphql soft-delete tree-recycle-root-only \
      'query($orgId:ID!){recycleBin(orgId:$orgId,input:{first:100}){nodes{lifecycleUnitId resourceType resourceId displayName deletedAt}pageInfo{hasNextPage endCursor}}}' \
      "$(jq -cn --arg orgId "$ORG_ID" '{orgId:$orgId}')"
    [[ "$(jq --arg root "$tree_a" --arg b "$tree_b" --arg c "$tree_c" --arg d "$tree_d" \
      '[.data.recycleBin.nodes[]|select(.resourceId==$root and .resourceType=="FOLDER")]|length' <<<"$PHASE13_LAST_JSON")" == "1" ]] \
      || phase13_die "Recycle Bin did not expose exactly the tree root"
    [[ "$(jq --arg b "$tree_b" --arg c "$tree_c" --arg d "$tree_d" \
      '[.data.recycleBin.nodes[]|select(.resourceId==$b or .resourceId==$c or .resourceId==$d)]|length' <<<"$PHASE13_LAST_JSON")" == "0" ]] \
      || phase13_die "Recycle Bin exposed a tree descendant"
    phase13_create_named_folder soft-delete tree-create-root-collision "$RUN_ID tree A"
    tree_collision="$PHASE13_LAST_FOLDER_ID"
    phase13_restore_unit_and_wait soft-delete tree-restore-with-collision "$tree_unit"
    [[ "$(phase13_asset_psql "SELECT name FROM folders WHERE id='$tree_a'::uuid AND deleted_at IS NULL;")" == "$RUN_ID tree A (1)" ]] \
      || phase13_die "Restored tree root did not receive the expected collision suffix"
    [[ "$(phase13_asset_psql "SELECT count(*) FROM folders WHERE id IN ('$tree_a'::uuid,'$tree_b'::uuid,'$tree_c'::uuid) AND deleted_at IS NULL AND lifecycle_unit_id IS NULL;")" == "3" ]] \
      || phase13_die "The complete folder tree was not restored"
    [[ "$(phase13_asset_psql "SELECT count(*) FROM metadata_items WHERE id='$tree_d'::uuid AND deleted_at IS NULL AND lifecycle_unit_id IS NULL;")" == "1" ]] \
      || phase13_die "The tree metadata was not restored"
    phase13_record_asset_query soft-delete tree-restored-with-collision \
      "SELECT json_build_object('sourceRoot',(SELECT name FROM folders WHERE id='$tree_a'::uuid),'collisionRoot',(SELECT name FROM folders WHERE id='$tree_collision'::uuid),'unitState',(SELECT state FROM asset_lifecycle_units WHERE id='$tree_unit'::uuid));" >/dev/null
    phase13_case_pass

    phase13_pause "1.12 Create nested independent deletion roots and enforce parent-first restore"
    phase13_create_named_folder soft-delete nested-create-a "$RUN_ID nested A"
    nested_a="$PHASE13_LAST_FOLDER_ID"
    nested_a_path="$PHASE13_LAST_FOLDER_PATH"
    phase13_create_named_folder soft-delete nested-create-b "$RUN_ID nested B" "$nested_a_path"
    nested_b="$PHASE13_LAST_FOLDER_ID"
    phase13_create_named_folder soft-delete nested-create-c "$RUN_ID nested C" "$nested_a_path"
    nested_c="$PHASE13_LAST_FOLDER_ID"
    nested_d="$(phase13_create_metadata_in_folder soft-delete nested-create-d "$nested_c" "$RUN_ID nested D")"
    phase13_delete_folder_tree soft-delete nested-delete-b "$nested_b"
    nested_b_unit="$PHASE13_LAST_FOLDER_UNIT"
    phase13_delete_folder_tree soft-delete nested-delete-a "$nested_a"
    nested_a_unit="$PHASE13_LAST_FOLDER_UNIT"
    [[ "$nested_a_unit" != "$nested_b_unit" ]] || phase13_die "Nested deletes did not retain independent lifecycle roots"
    phase13_graphql_expect_error_as "$ADMIN_USER" soft-delete nested-restore-b-before-a RESTORE_PARENT_DELETED \
      'mutation($orgId:ID!,$unitId:ID!){restoreLifecycleUnit(orgId:$orgId,unitId:$unitId){jobId status}}' \
      "$(jq -cn --arg orgId "$ORG_ID" --arg unitId "$nested_b_unit" '{orgId:$orgId,unitId:$unitId}')"
    [[ "$(phase13_asset_psql "SELECT count(*) FROM asset_lifecycle_jobs WHERE unit_id='$nested_b_unit'::uuid AND operation='RESTORE';")" == "0" ]] \
      || phase13_die "Blocked child restore created a durable job"
    phase13_restore_unit_and_wait soft-delete nested-restore-a "$nested_a_unit"
    [[ "$(phase13_asset_psql "SELECT count(*) FROM folders WHERE id IN ('$nested_a'::uuid,'$nested_c'::uuid) AND deleted_at IS NULL;")" == "2" ]] \
      || phase13_die "Parent restore did not restore A and C"
    [[ "$(phase13_asset_psql "SELECT count(*) FROM metadata_items WHERE id='$nested_d'::uuid AND deleted_at IS NULL;")" == "1" ]] \
      || phase13_die "Parent restore did not restore D"
    [[ "$(phase13_asset_psql "SELECT count(*) FROM folders WHERE id='$nested_b'::uuid AND deleted_at IS NOT NULL AND lifecycle_unit_id='$nested_b_unit'::uuid;")" == "1" ]] \
      || phase13_die "Parent restore incorrectly restored independently deleted B"
    phase13_record_asset_query soft-delete nested-parent-first-state \
      "SELECT json_build_object('unitA',(SELECT state FROM asset_lifecycle_units WHERE id='$nested_a_unit'::uuid),'unitB',(SELECT state FROM asset_lifecycle_units WHERE id='$nested_b_unit'::uuid),'activeA',(SELECT deleted_at IS NULL FROM folders WHERE id='$nested_a'::uuid),'activeC',(SELECT deleted_at IS NULL FROM folders WHERE id='$nested_c'::uuid),'activeD',(SELECT deleted_at IS NULL FROM metadata_items WHERE id='$nested_d'::uuid),'deletedB',(SELECT deleted_at IS NOT NULL FROM folders WHERE id='$nested_b'::uuid));" >/dev/null
    phase13_restore_unit_and_wait soft-delete nested-restore-b-after-a "$nested_b_unit"
    phase13_case_pass

    phase13_pause "1.13 Restore metadata without overwrite by applying the collision suffix"
    collision_source="$(phase13_create_metadata_in_folder soft-delete collision-create-source "$PHASE13_FOLDER_ID" "$RUN_ID collision")"
    phase13_graphql soft-delete collision-delete-source \
      'mutation($orgId:ID!,$id:ID!){deleteMetadata(orgId:$orgId,id:$id)}' \
      "$(jq -cn --arg orgId "$ORG_ID" --arg id "$collision_source" '{orgId:$orgId,id:$id}')"
    collision_unit="$(phase13_asset_psql "SELECT id FROM asset_lifecycle_units WHERE root_resource_id='$collision_source'::uuid AND state='DELETED' ORDER BY created_at DESC LIMIT 1;")"
    collision_active="$(phase13_create_metadata_in_folder soft-delete collision-create-active "$PHASE13_FOLDER_ID" "$RUN_ID collision")"
    phase13_restore_unit_and_wait soft-delete collision-restore-source "$collision_unit"
    restored_title="$(phase13_asset_psql "SELECT title FROM metadata_items WHERE id='$collision_source'::uuid;")"
    [[ "$restored_title" == "$RUN_ID collision (1)" ]] || phase13_die "Restored metadata did not receive the expected collision suffix"
    [[ "$(phase13_asset_psql "SELECT count(*) FROM metadata_items WHERE id='$collision_active'::uuid AND title='$RUN_ID collision' AND deleted_at IS NULL;")" == "1" ]] \
      || phase13_die "Restore overwrote the active collision target"
    phase13_record_asset_query soft-delete metadata-collision-state \
      "SELECT json_build_object('restoredTitle',(SELECT title FROM metadata_items WHERE id='$collision_source'::uuid),'existingTitle',(SELECT title FROM metadata_items WHERE id='$collision_active'::uuid),'unitState',(SELECT state FROM asset_lifecycle_units WHERE id='$collision_unit'::uuid));" >/dev/null
    phase13_case_pass

    phase13_pause "1.14 Enforce read, write, no-access, and organization-admin permission boundaries"
    viewer_user="00000000-0000-0000-0000-000000000002"
    no_access_user="$(phase13_uuid)"
    PHASE13_EPHEMERAL_ACCESS_USER="$no_access_user"
    phase13_access_psql \
      "INSERT INTO access.users(id,email,display_name,is_active) VALUES ('$no_access_user'::uuid,'$no_access_user@phase13.invalid','Phase 1.3 no access',true); INSERT INTO access.organization_members(id,org_id,user_id) VALUES (gen_random_uuid(),'$ORG_ID'::uuid,'$no_access_user'::uuid);" >/dev/null
    permission_asset="$(phase13_create_metadata_in_folder soft-delete permission-create "$PHASE13_FOLDER_ID" "$RUN_ID permission")"
    phase13_graphql soft-delete permission-admin-delete \
      'mutation($orgId:ID!,$id:ID!){deleteMetadata(orgId:$orgId,id:$id)}' \
      "$(jq -cn --arg orgId "$ORG_ID" --arg id "$permission_asset" '{orgId:$orgId,id:$id}')"
    permission_unit="$(phase13_asset_psql "SELECT id FROM asset_lifecycle_units WHERE root_resource_id='$permission_asset'::uuid AND state='DELETED' ORDER BY created_at DESC LIMIT 1;")"
    phase13_graphql_as "$viewer_user" soft-delete permission-viewer-read \
      'query($orgId:ID!){recycleBin(orgId:$orgId,input:{first:100}){nodes{lifecycleUnitId resourceId resourceType}pageInfo{hasNextPage}}}' \
      "$(jq -cn --arg orgId "$ORG_ID" '{orgId:$orgId}')"
    [[ "$(jq --arg id "$permission_asset" '[.data.recycleBin.nodes[]|select(.resourceId==$id)]|length' <<<"$PHASE13_LAST_JSON")" == "1" ]] \
      || phase13_die "Read-only viewer could not see the permitted Recycle Bin entry"
    phase13_graphql_expect_error_as "$viewer_user" soft-delete permission-viewer-delete-denied FORBIDDEN \
      'mutation($orgId:ID!,$id:ID!){deleteMetadata(orgId:$orgId,id:$id)}' \
      "$(jq -cn --arg orgId "$ORG_ID" --arg id "$collision_active" '{orgId:$orgId,id:$id}')"
    phase13_graphql_expect_error_as "$viewer_user" soft-delete permission-viewer-restore-denied FORBIDDEN \
      'mutation($orgId:ID!,$unitId:ID!){restoreLifecycleUnit(orgId:$orgId,unitId:$unitId){jobId status}}' \
      "$(jq -cn --arg orgId "$ORG_ID" --arg unitId "$permission_unit" '{orgId:$orgId,unitId:$unitId}')"
    phase13_graphql_as "$no_access_user" soft-delete permission-no-access-read \
      'query($orgId:ID!){recycleBin(orgId:$orgId,input:{first:100}){nodes{resourceId resourceType}pageInfo{hasNextPage}}}' \
      "$(jq -cn --arg orgId "$ORG_ID" '{orgId:$orgId}')"
    [[ "$(jq --arg id "$permission_asset" '[.data.recycleBin.nodes[]|select(.resourceId==$id)]|length' <<<"$PHASE13_LAST_JSON")" == "0" ]] \
      || phase13_die "No-access member could see an unauthorized Recycle Bin entry"
    phase13_graphql_expect_error_as "$no_access_user" soft-delete permission-no-access-delete-denied FORBIDDEN \
      'mutation($orgId:ID!,$id:ID!){deleteMetadata(orgId:$orgId,id:$id)}' \
      "$(jq -cn --arg orgId "$ORG_ID" --arg id "$collision_active" '{orgId:$orgId,id:$id}')"
    phase13_restore_unit_and_wait soft-delete permission-admin-restore "$permission_unit"
    phase13_cleanup_ephemeral_access_user
    phase13_record_asset_query soft-delete permission-boundary-state \
      "SELECT json_build_object('adminRestored',deleted_at IS NULL,'unitState',(SELECT state FROM asset_lifecycle_units WHERE id='$permission_unit'::uuid)) FROM metadata_items WHERE id='$permission_asset'::uuid;" >/dev/null
    phase13_case_pass

    phase13_pause "1.15 Validate Recycle Bin pagination and malformed or stale cursor rejection"
    cursor_ids=()
    cursor_units=()
    for cursor_id in 1 2 3; do
        cursor_id="$(phase13_create_metadata_in_folder soft-delete "cursor-create-$cursor_id" "$PHASE13_FOLDER_ID" "$RUN_ID cursor $cursor_id")"
        cursor_ids+=("$cursor_id")
        phase13_graphql soft-delete cursor-delete \
          'mutation($orgId:ID!,$id:ID!){deleteMetadata(orgId:$orgId,id:$id)}' \
          "$(jq -cn --arg orgId "$ORG_ID" --arg id "$cursor_id" '{orgId:$orgId,id:$id}')"
        cursor_unit="$(phase13_asset_psql "SELECT id FROM asset_lifecycle_units WHERE root_resource_id='$cursor_id'::uuid AND state='DELETED' ORDER BY created_at DESC LIMIT 1;")"
        cursor_units+=("$cursor_unit")
        sleep 0.05
    done
    phase13_graphql soft-delete cursor-first-page \
      'query($orgId:ID!){recycleBin(orgId:$orgId,input:{first:1}){nodes{lifecycleUnitId resourceId resourceType}pageInfo{hasNextPage endCursor}}}' \
      "$(jq -cn --arg orgId "$ORG_ID" '{orgId:$orgId}')"
    cursor_first_id="$(jq -r '.data.recycleBin.nodes[0].resourceId // empty' <<<"$PHASE13_LAST_JSON")"
    cursor_first_unit="$(jq -r '.data.recycleBin.nodes[0].lifecycleUnitId // empty' <<<"$PHASE13_LAST_JSON")"
    cursor_end="$(jq -r '.data.recycleBin.pageInfo.endCursor // empty' <<<"$PHASE13_LAST_JSON")"
    [[ "$(jq -r '.data.recycleBin.pageInfo.hasNextPage' <<<"$PHASE13_LAST_JSON")" == "true" && -n "$cursor_end" ]] \
      || phase13_die "First cursor page did not expose hasNextPage and endCursor"
    [[ " ${cursor_ids[*]} " == *" $cursor_first_id "* ]] || phase13_die "Newest Recycle Bin entry was not owned by this demo run"
    phase13_graphql soft-delete cursor-second-page \
      'query($orgId:ID!,$after:String!){recycleBin(orgId:$orgId,input:{first:1,after:$after}){nodes{lifecycleUnitId resourceId resourceType}pageInfo{hasNextPage endCursor}}}' \
      "$(jq -cn --arg orgId "$ORG_ID" --arg after "$cursor_end" '{orgId:$orgId,after:$after}')"
    cursor_second_id="$(jq -r '.data.recycleBin.nodes[0].resourceId // empty' <<<"$PHASE13_LAST_JSON")"
    [[ -n "$cursor_second_id" && "$cursor_second_id" != "$cursor_first_id" ]] || phase13_die "Cursor page repeated or omitted the next entry"
    phase13_graphql_expect_error_as "$ADMIN_USER" soft-delete cursor-malformed CURSOR_INVALID \
      'query($orgId:ID!){recycleBin(orgId:$orgId,input:{first:1,after:"not-a-valid-cursor"}){nodes{resourceId}}}' \
      "$(jq -cn --arg orgId "$ORG_ID" '{orgId:$orgId}')"
    phase13_restore_unit_and_wait soft-delete cursor-restore-anchor "$cursor_first_unit"
    phase13_graphql_expect_error_as "$ADMIN_USER" soft-delete cursor-stale CURSOR_INVALID \
      'query($orgId:ID!,$after:String!){recycleBin(orgId:$orgId,input:{first:1,after:$after}){nodes{resourceId}}}' \
      "$(jq -cn --arg orgId "$ORG_ID" --arg after "$cursor_end" '{orgId:$orgId,after:$after}')"
    for cursor_unit in "${cursor_units[@]}"; do
        if [[ "$cursor_unit" != "$cursor_first_unit" ]]; then
            phase13_restore_unit_and_wait soft-delete cursor-cleanup-restore "$cursor_unit"
        fi
    done
    phase13_record_asset_query soft-delete cursor-contract-state \
      "SELECT json_build_object('firstResource','$cursor_first_id','secondResource','$cursor_second_id','restoredUnits',(SELECT count(*) FROM asset_lifecycle_units WHERE id IN ('${cursor_units[0]}'::uuid,'${cursor_units[1]}'::uuid,'${cursor_units[2]}'::uuid) AND state='RESTORED'));" >/dev/null
    phase13_case_pass
    phase13_capture_service_logs soft-delete "$ACCESS_CORE_CONTAINER" "$ASSET_CORE_CONTAINER" "$DELETE_WORKER_CONTAINER"
    phase13_log "Metadata lifecycle, folder-tree deletion, parent-first restore, collision, permissions, cursor, cleanup, and purge all passed."
}
