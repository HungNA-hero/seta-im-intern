#!/usr/bin/env bash

phase13_wait_media_terminal() {
    local asset_id="$1" job_id="$2" step="$3" expected="$4" timeout="${5:-120}"
    local deadline=$((SECONDS + timeout)) response status current_job
    while (( SECONDS < deadline )); do
        response="$(curl -fsS --max-time 10 "$ACCESS_URL/api/v1/assets/$asset_id/media/status" \
          -H "x-user-id: $ADMIN_USER" -H "x-org-id: $ORG_ID")" || { sleep 0.25; continue; }
        current_job="$(jq -r '.data.jobId // empty' <<<"$response")"
        status="$(jq -r '.data.status // empty' <<<"$response")"
        if [[ "$current_job" == "$job_id" && "$status" == "$expected" ]]; then
            PHASE13_LAST_JSON="$response"
            phase13_record_json upload api-responses "$step" \
              "$(jq -cn --arg status 200 --argjson body "$response" '{httpStatus:($status|tonumber),body:$body}')"
            return 0
        fi
        if [[ "$current_job" == "$job_id" && "$status" == "FAILED" && "$expected" != "FAILED" ]]; then
            phase13_die "Media job $job_id failed: $(jq -r '.data.error.code // "unknown"' <<<"$response")"
            return 1
        fi
        sleep 0.25
    done
    phase13_die "Timed out waiting for media job $job_id to reach $expected"
}

phase13_wait_retry_state() {
    local job_id="$1" timeout="${2:-45}" deadline state
    deadline=$((SECONDS + timeout))
    while (( SECONDS < deadline )); do
        state="$(phase13_asset_psql "SELECT status||'|'||attempt_count||'|'||CASE WHEN next_attempt_at>statement_timestamp() THEN 'true' ELSE 'false' END||'|'||(SELECT count(*) FROM media_job_outbox WHERE job_id='$job_id'::uuid) FROM media_processing_jobs WHERE id='$job_id'::uuid;")" || return 1
        if [[ "$state" == queued\|1\|true\|2 || "$state" =~ ^queued\|1\|true\|[3-9][0-9]*$ ]]; then
            printf '%s\n' "$state"
            return 0
        fi
        if [[ "$state" =~ ^failed\| ]]; then
            phase13_die "Media job $job_id became terminal instead of retrying: $state"
            return 1
        fi
        sleep 0.1
    done
    phase13_die "Timed out waiting for first durable retry of media job $job_id; last state: ${state:-<empty>}"
}

phase13_assert_media_activation() {
    local asset_id="$1" job_id="$2" expected_attempts="$3" row
    row="$(phase13_asset_psql "SELECT (SELECT count(*) FROM media_processing_jobs WHERE asset_id='$asset_id'::uuid)||'|'||(SELECT count(*) FROM asset_media_versions WHERE asset_id='$asset_id'::uuid)||'|'||(SELECT count(*) FROM media_outputs WHERE version_id=j.version_id)||'|'||EXISTS(SELECT 1 FROM metadata_items m WHERE m.id='$asset_id'::uuid AND m.active_media_version_id=j.version_id AND m.pending_media_version_id IS NULL)||'|'||j.attempt_count FROM media_processing_jobs j WHERE j.id='$job_id'::uuid;")"
    [[ "$row" == "1|1|2|true|$expected_attempts" ]] || phase13_die "Media activation invariant failed for $job_id: $row"
}

phase13_run_media_upload() {
    local fixture="$REPO_ROOT/services/asset-core/testdata/media/valid/small-64x64.png"
    local landscape="$REPO_ROOT/services/asset-core/testdata/media/valid/landscape-2048x1152.jpg"
    local hostile="$REPO_ROOT/services/asset-core/testdata/media/hostile/jpeg-trailing-payload.jpg"
    local happy_asset happy_session happy_upload happy_job happy_key raw_key raw_status thumb_status web_status
    local retry_asset retry_session retry_upload retry_job retry_state attempt_after_stop
    local replay_upload replay_job active_before active_after failed_session failed_upload failed_job
    local replacement_session replacement_upload replacement_job replacement_version permission_body
    local viewer_user no_access_user no_access_key

    [[ -f "$fixture" && -f "$landscape" && -f "$hostile" ]] || phase13_die "One or more media demo fixtures are missing"

    phase13_pause "2.1 Create a run-owned asset for the successful upload"
    phase13_create_demo_folder upload
    happy_asset="$(phase13_create_metadata upload "$RUN_ID upload happy path")"
    phase13_log "Created happy-path asset: $happy_asset"
    phase13_case_pass

    phase13_pause "2.2 Declare checksum and size, then create a presigned upload session"
    phase13_create_upload_session create-happy-session "$happy_asset" "$fixture" small-64x64.png
    happy_session="$PHASE13_LAST_JSON"
    happy_upload="$(jq -r '.data.uploadId' <<<"$happy_session")"
    happy_key="$PHASE13_LAST_IDEMPOTENCY_KEY"
    [[ "$happy_upload" =~ ^[0-9a-f-]{36}$ ]] || phase13_die "Happy upload session returned an invalid ID"
    phase13_log "Upload session $happy_upload admitted $(wc -c <"$fixture" | tr -d ' ') bytes. Signed URL is redacted from evidence."
    phase13_case_pass

    phase13_pause "2.3 PUT the image directly to private object storage"
    phase13_transfer_upload transfer-happy "$happy_session" "$fixture"
    raw_key="$(phase13_asset_psql "SELECT raw_object_key FROM media_upload_sessions WHERE id='$happy_upload'::uuid;")"
    raw_status="$(curl -sS --max-time 10 -o /dev/null -w '%{http_code}' "$MINIO_URL/${ASSET_MEDIA_BUCKET:-seta-media}/$raw_key")"
    phase13_record_json upload api-responses anonymous-raw-denied \
      "$(jq -cn --arg status "$raw_status" '{httpStatus:($status|tonumber),url:"PRIVATE_RAW_OBJECT?REDACTED"}')"
    [[ "$raw_status" == "403" ]] || phase13_die "Anonymous raw object read returned HTTP $raw_status, expected 403"
    phase13_case_pass

    phase13_pause "2.4 Commit once and wait for authoritative completion"
    phase13_http_json upload commit-happy PUT "$ACCESS_URL/api/v1/assets/$happy_asset/media" 202 \
      "$(jq -cn --arg uploadId "$happy_upload" '{uploadId:$uploadId}')"
    happy_job="$(jq -r '.data.jobId' <<<"$PHASE13_LAST_JSON")"
    phase13_wait_media_terminal "$happy_asset" "$happy_job" happy-status-completed COMPLETED 120
    [[ "$(jq '[.data.outputs.thumbnail,.data.outputs.web]|map(select(. != null))|length' <<<"$PHASE13_LAST_JSON")" == "2" ]] \
      || phase13_die "Completed status did not expose exactly thumbnail and web outputs"
    thumb_status="$(curl -sS --max-time 15 -o /dev/null -w '%{http_code}' "$(jq -r '.data.outputs.thumbnail.url' <<<"$PHASE13_LAST_JSON")")"
    web_status="$(curl -sS --max-time 15 -o /dev/null -w '%{http_code}' "$(jq -r '.data.outputs.web.url' <<<"$PHASE13_LAST_JSON")")"
    [[ "$thumb_status" == "200" && "$web_status" == "200" ]] || phase13_die "Signed derivative reads failed: thumbnail=$thumb_status web=$web_status"
    phase13_assert_media_activation "$happy_asset" "$happy_job" 1
    phase13_record_asset_query upload happy-durable-state \
      "SELECT json_build_object('jobId',j.id,'status',j.status,'attemptCount',j.attempt_count,'versionId',j.version_id,'outputCount',(SELECT count(*) FROM media_outputs WHERE version_id=j.version_id),'activeVersionId',m.active_media_version_id,'pendingVersionId',m.pending_media_version_id) FROM media_processing_jobs j JOIN metadata_items m ON m.id=j.asset_id WHERE j.id='$happy_job'::uuid;" >/dev/null
    phase13_log "Happy path completed with one version, one job, and exactly two outputs."
    phase13_case_pass

    phase13_pause "2.5 Create and transfer a second upload for retry recovery"
    retry_asset="$(phase13_create_metadata upload "$RUN_ID upload retry recovery")"
    phase13_create_upload_session create-retry-session "$retry_asset" "$fixture" small-64x64.png
    retry_session="$PHASE13_LAST_JSON"
    retry_upload="$(jq -r '.data.uploadId' <<<"$retry_session")"
    phase13_transfer_upload transfer-retry "$retry_session" "$fixture"
    phase13_case_pass

    phase13_pause "2.6 Stop the media worker before commit to eliminate the initial-claim race"
    phase13_stop_media_worker
    phase13_http_json upload commit-retry PUT "$ACCESS_URL/api/v1/assets/$retry_asset/media" 202 \
      "$(jq -cn --arg uploadId "$retry_upload" '{uploadId:$uploadId}')"
    retry_job="$(jq -r '.data.jobId' <<<"$PHASE13_LAST_JSON")"
    [[ "$(phase13_asset_psql "SELECT status||'|'||attempt_count||'|'||(SELECT count(*) FROM media_job_outbox WHERE job_id='$retry_job'::uuid AND status IN ('pending','publishing')) FROM media_processing_jobs WHERE id='$retry_job'::uuid;")" == "queued|0|1" ]] \
      || phase13_die "Commit did not create exactly one durable queued job and unpublished outbox event"
    phase13_case_pass

    phase13_pause "2.7 Stop MinIO, start the worker, and observe one transient retry"
    phase13_stop_minio
    phase13_start_media_worker
    retry_state="$(phase13_wait_retry_state "$retry_job" 60)"
    phase13_log "Observed durable retry state: $retry_state"
    phase13_record_asset_query upload retry-after-storage-failure \
      "SELECT json_build_object('jobId',j.id,'status',j.status,'attemptCount',j.attempt_count,'nextAttemptAt',j.next_attempt_at,'lastErrorCode',j.last_error_code,'outboxRows',(SELECT count(*) FROM media_job_outbox WHERE job_id=j.id)) FROM media_processing_jobs j WHERE j.id='$retry_job'::uuid;" >/dev/null
    phase13_case_pass

    phase13_pause "2.8 Stop the worker while queued and prove no extra attempt was consumed"
    phase13_stop_media_worker
    attempt_after_stop="$(phase13_asset_psql "SELECT status||'|'||attempt_count FROM media_processing_jobs WHERE id='$retry_job'::uuid;")"
    [[ "$attempt_after_stop" == "queued|1" ]] || phase13_die "Expected queued attempt 1 after stopping worker, got $attempt_after_stop"
    phase13_case_pass

    phase13_pause "2.9 Restore MinIO and the worker; do not recommit"
    phase13_start_minio
    phase13_start_media_worker
    phase13_wait_media_terminal "$retry_asset" "$retry_job" retry-status-completed COMPLETED 120
    phase13_assert_media_activation "$retry_asset" "$retry_job" 2
    phase13_record_asset_query upload retry-recovered-state \
      "SELECT json_build_object('jobId',j.id,'status',j.status,'attemptCount',j.attempt_count,'versionCount',(SELECT count(*) FROM asset_media_versions WHERE asset_id=j.asset_id),'jobCount',(SELECT count(*) FROM media_processing_jobs WHERE asset_id=j.asset_id),'outputCount',(SELECT count(*) FROM media_outputs WHERE version_id=j.version_id),'activeVersionId',m.active_media_version_id,'pendingVersionId',m.pending_media_version_id) FROM media_processing_jobs j JOIN metadata_items m ON m.id=j.asset_id WHERE j.id='$retry_job'::uuid;" >/dev/null
    phase13_case_pass

    phase13_pause "2.10 Verify the two derivative contracts without upscaling the small source"
    [[ "$(phase13_asset_psql "SELECT count(*) FROM media_outputs o JOIN media_processing_jobs j ON j.version_id=o.version_id WHERE j.id='$happy_job'::uuid AND o.kind IN ('thumbnail','web') AND o.content_type='image/png' AND o.width=64 AND o.height=64 AND o.size_bytes>0;")" == "2" ]] \
      || phase13_die "Small-source derivatives violated count, format, bounds, or no-upscale behavior"
    [[ "$(phase13_asset_psql "SELECT source_width||'x'||source_height||'|'||detected_content_type FROM asset_media_versions v JOIN media_processing_jobs j ON j.version_id=v.id WHERE j.id='$happy_job'::uuid;")" == "64x64|image/png" ]] \
      || phase13_die "The trusted source dimensions or detected format were incorrect"
    phase13_record_asset_query upload output-contract \
      "SELECT json_build_object('source',json_build_object('width',v.source_width,'height',v.source_height,'contentType',v.detected_content_type),'outputs',(SELECT json_agg(json_build_object('kind',o.kind,'width',o.width,'height',o.height,'contentType',o.content_type,'sizeBytes',o.size_bytes) ORDER BY o.kind) FROM media_outputs o WHERE o.version_id=v.id),'rawPrivate',true) FROM asset_media_versions v JOIN media_processing_jobs j ON j.version_id=v.id WHERE j.id='$happy_job'::uuid;" >/dev/null
    phase13_case_pass

    phase13_pause "2.11 Replay session creation and commit without duplicate durable effects"
    phase13_create_upload_session replay-happy-session "$happy_asset" "$fixture" small-64x64.png "$happy_key" 200
    replay_upload="$(jq -r '.data.uploadId' <<<"$PHASE13_LAST_JSON")"
    [[ "$replay_upload" == "$happy_upload" && "$PHASE13_LAST_IDEMPOTENCY_REPLAYED" == "true" ]] \
      || phase13_die "Identical idempotency replay did not return the original upload and replay header"
    phase13_create_upload_session conflict-happy-session "$happy_asset" "$fixture" renamed-64x64.png "$happy_key" 409
    [[ "$(jq -r '.error.code // empty' <<<"$PHASE13_LAST_JSON")" == "IDEMPOTENCY_KEY_REUSED" ]] \
      || phase13_die "Changed idempotency payload did not return IDEMPOTENCY_KEY_REUSED"
    phase13_http_json upload replay-happy-commit PUT "$ACCESS_URL/api/v1/assets/$happy_asset/media" 200 \
      "$(jq -cn --arg uploadId "$happy_upload" '{uploadId:$uploadId}')"
    replay_job="$(jq -r '.data.jobId' <<<"$PHASE13_LAST_JSON")"
    [[ "$replay_job" == "$happy_job" && "$PHASE13_LAST_IDEMPOTENCY_REPLAYED" == "true" ]] \
      || phase13_die "Repeated commit did not return the original job and replay header"
    [[ "$(phase13_asset_psql "SELECT (SELECT count(*) FROM media_upload_sessions WHERE asset_id='$happy_asset'::uuid)||'|'||(SELECT count(*) FROM asset_media_versions WHERE asset_id='$happy_asset'::uuid)||'|'||(SELECT count(*) FROM media_processing_jobs WHERE asset_id='$happy_asset'::uuid);")" == "1|1|1" ]] \
      || phase13_die "Idempotent replay created duplicate session, version, or job rows"
    phase13_record_asset_query upload idempotency-state \
      "SELECT json_build_object('uploadId','$happy_upload','jobId','$happy_job','sessions',(SELECT count(*) FROM media_upload_sessions WHERE asset_id='$happy_asset'::uuid),'versions',(SELECT count(*) FROM asset_media_versions WHERE asset_id='$happy_asset'::uuid),'jobs',(SELECT count(*) FROM media_processing_jobs WHERE asset_id='$happy_asset'::uuid));" >/dev/null
    phase13_case_pass

    phase13_pause "2.12 Preserve active media through a failed replacement, then switch atomically"
    active_before="$(phase13_asset_psql "SELECT active_media_version_id FROM metadata_items WHERE id='$happy_asset'::uuid;")"
    [[ "$active_before" =~ ^[0-9a-f-]{36}$ ]] || phase13_die "Happy asset had no active version before replacement"
    phase13_create_upload_session create-failed-replacement "$happy_asset" "$hostile" jpeg-trailing-payload.jpg "" 201 image/jpeg
    failed_session="$PHASE13_LAST_JSON"
    failed_upload="$(jq -r '.data.uploadId' <<<"$failed_session")"
    phase13_transfer_upload transfer-failed-replacement "$failed_session" "$hostile"
    phase13_http_json upload commit-failed-replacement PUT "$ACCESS_URL/api/v1/assets/$happy_asset/media" 202 \
      "$(jq -cn --arg uploadId "$failed_upload" '{uploadId:$uploadId}')"
    failed_job="$(jq -r '.data.jobId' <<<"$PHASE13_LAST_JSON")"
    phase13_wait_media_terminal "$happy_asset" "$failed_job" failed-replacement-status FAILED 120
    [[ "$(jq -r '.data.outputs' <<<"$PHASE13_LAST_JSON")" == "null" && -n "$(jq -r '.data.error.code // empty' <<<"$PHASE13_LAST_JSON")" ]] \
      || phase13_die "Failed replacement exposed output or omitted its safe error"
    [[ "$(jq -c '.data.error' <<<"$PHASE13_LAST_JSON")" != *"/tmp/"* ]] || phase13_die "Failed replacement exposed an internal path"
    [[ "$(phase13_asset_psql "SELECT active_media_version_id||'|'||COALESCE(pending_media_version_id::text,'null') FROM metadata_items WHERE id='$happy_asset'::uuid;")" == "$active_before|null" ]] \
      || phase13_die "Failed replacement changed the active version or left a pending pointer"
    [[ "$(phase13_asset_psql "SELECT j.attempt_count||'|'||(SELECT count(*) FROM media_outputs WHERE version_id=j.version_id) FROM media_processing_jobs j WHERE j.id='$failed_job'::uuid;")" == "1|0" ]] \
      || phase13_die "Deterministic replacement failure retried or exposed derivative rows"
    phase13_create_upload_session create-successful-replacement "$happy_asset" "$landscape" landscape-2048x1152.jpg "" 201 image/jpeg
    replacement_session="$PHASE13_LAST_JSON"
    replacement_upload="$(jq -r '.data.uploadId' <<<"$replacement_session")"
    phase13_transfer_upload transfer-successful-replacement "$replacement_session" "$landscape"
    phase13_http_json upload commit-successful-replacement PUT "$ACCESS_URL/api/v1/assets/$happy_asset/media" 202 \
      "$(jq -cn --arg uploadId "$replacement_upload" '{uploadId:$uploadId}')"
    replacement_job="$(jq -r '.data.jobId' <<<"$PHASE13_LAST_JSON")"
    phase13_wait_media_terminal "$happy_asset" "$replacement_job" successful-replacement-status COMPLETED 120
    active_after="$(phase13_asset_psql "SELECT active_media_version_id FROM metadata_items WHERE id='$happy_asset'::uuid;")"
    replacement_version="$(phase13_asset_psql "SELECT version_id FROM media_processing_jobs WHERE id='$replacement_job'::uuid;")"
    [[ "$active_after" == "$replacement_version" && "$active_after" != "$active_before" ]] \
      || phase13_die "Successful replacement did not atomically switch the active version"
    [[ "$(phase13_asset_psql "SELECT count(*) FROM asset_media_versions WHERE id='$active_before'::uuid AND status='completed';")" == "1" ]] \
      || phase13_die "Successful replacement did not retain the previous completed version"
    [[ "$(phase13_asset_psql "SELECT count(*) FROM media_outputs WHERE version_id='$active_before'::uuid;")" == "2" ]] \
      || phase13_die "Successful replacement removed the previous safe outputs"
    [[ "$(phase13_asset_psql "SELECT count(*) FROM media_outputs WHERE version_id='$replacement_version'::uuid AND ((kind='thumbnail' AND width<=256 AND height<=256) OR (kind='web' AND width<=1080 AND height<=1080)) AND content_type='image/jpeg';")" == "2" ]] \
      || phase13_die "Successful replacement outputs violated the JPEG derivative contract"
    phase13_record_asset_query upload replacement-state \
      "SELECT json_build_object('activeBefore','$active_before','failedJob',(SELECT json_build_object('status',status,'attempts',attempt_count,'outputCount',(SELECT count(*) FROM media_outputs WHERE version_id=media_processing_jobs.version_id)) FROM media_processing_jobs WHERE id='$failed_job'::uuid),'activeAfter','$active_after','previousRetained',(SELECT status FROM asset_media_versions WHERE id='$active_before'::uuid),'successfulOutputs',(SELECT json_agg(json_build_object('kind',kind,'width',width,'height',height,'contentType',content_type) ORDER BY kind) FROM media_outputs WHERE version_id='$replacement_version'::uuid));" >/dev/null
    phase13_case_pass

    phase13_pause "2.13 Enforce media create, commit, status, and output permission boundaries"
    viewer_user="00000000-0000-0000-0000-000000000002"
    phase13_access_psql "SELECT 1 FROM access.organization_members WHERE org_id='$ORG_ID'::uuid AND user_id='$viewer_user'::uuid LIMIT 1;" \
      | grep -qx 1 || phase13_die "Read-only viewer fixture is missing"
    no_access_user="$(phase13_uuid)"
    no_access_key="$(phase13_uuid)"
    PHASE13_EPHEMERAL_ACCESS_USER="$no_access_user"
    phase13_access_psql \
      "INSERT INTO access.users(id,email,display_name,is_active) VALUES ('$no_access_user'::uuid,'$no_access_user@phase13.invalid','Phase 1.3 media no access',true); INSERT INTO access.organization_members(id,org_id,user_id) VALUES (gen_random_uuid(),'$ORG_ID'::uuid,'$no_access_user'::uuid);" >/dev/null
    permission_body="$(jq -cn --arg checksum "$(openssl dgst -sha256 -binary "$fixture" | base64 | tr -d '\n')" --argjson size "$(wc -c <"$fixture" | tr -d ' ')" \
      '{filename:"permission.png",contentType:"image/png",sizeBytes:$size,checksumSha256:$checksum}')"
    phase13_http_json_as "$viewer_user" upload permission-viewer-create-denied POST \
      "$ACCESS_URL/api/v1/assets/$happy_asset/media/uploads" 403 "$permission_body" "$(phase13_uuid)"
    [[ "$(jq -r '.error.code // empty' <<<"$PHASE13_LAST_JSON")" == "FORBIDDEN" ]] || phase13_die "Viewer create was not denied safely"
    phase13_http_json_as "$viewer_user" upload permission-viewer-commit-denied PUT \
      "$ACCESS_URL/api/v1/assets/$happy_asset/media" 403 "$(jq -cn --arg uploadId "$replacement_upload" '{uploadId:$uploadId}')"
    phase13_http_json_as "$viewer_user" upload permission-viewer-status-denied GET \
      "$ACCESS_URL/api/v1/assets/$happy_asset/media/status" 403
    phase13_http_json_as "$no_access_user" upload permission-no-access-create-denied POST \
      "$ACCESS_URL/api/v1/assets/$happy_asset/media/uploads" 403 "$permission_body" "$no_access_key"
    phase13_http_json_as "$no_access_user" upload permission-no-access-status-denied GET \
      "$ACCESS_URL/api/v1/assets/$happy_asset/media/status" 403
    phase13_http_json upload permission-admin-status GET "$ACCESS_URL/api/v1/assets/$happy_asset/media/status" 200
    [[ "$(jq -r '.data.jobId' <<<"$PHASE13_LAST_JSON")" == "$replacement_job" ]] \
      || phase13_die "Organization admin did not receive authoritative replacement status"
    [[ "$(jq '[.data.outputs.thumbnail.url,.data.outputs.web.url]|map(select(type=="string" and length>0))|length' <<<"$PHASE13_LAST_JSON")" == "2" ]] \
      || phase13_die "Authorized status did not expose exactly two temporary output links"
    phase13_cleanup_ephemeral_access_user
    phase13_record_asset_query upload permission-state \
      "SELECT json_build_object('assetId','$happy_asset','activeVersionId',active_media_version_id,'pendingVersionId',pending_media_version_id,'adminStatusVisible',true,'unauthorizedSessionRows',(SELECT count(*) FROM media_upload_sessions WHERE requested_by='$no_access_user'::uuid)) FROM metadata_items WHERE id='$happy_asset'::uuid;" >/dev/null
    phase13_case_pass
    phase13_capture_service_logs upload "$ACCESS_CORE_CONTAINER" "$ASSET_CORE_CONTAINER" "$MEDIA_WORKER_CONTAINER" "$MINIO_CONTAINER"
    phase13_log "Happy path, retry recovery, derivative contract, idempotency, no-loss replacement, and permission boundaries all passed."
    phase13_log "Run-owned metadata and object evidence is intentionally preserved for inspection."
}
