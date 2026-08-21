#!/usr/bin/env bash

phase13_run_soft_delete_cleanup() {
    local asset_id first_unit second_unit restore_job cleanup_run purge_job
    local unrelated_eligible active_runs baseline_other_units baseline_other_metadata after_other_units after_other_metadata

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
    phase13_log "Demo-only scheduler trigger created durable cleanup run $cleanup_run."
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
    phase13_capture_service_logs soft-delete "$ACCESS_CORE_CONTAINER" "$ASSET_CORE_CONTAINER" "$DELETE_WORKER_CONTAINER"
    phase13_log "Soft-delete, restore, re-delete, retention cleanup, and purge all passed."
}
