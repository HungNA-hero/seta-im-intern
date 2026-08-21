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
    local happy_asset happy_session happy_upload happy_job raw_key raw_status thumb_status web_status
    local retry_asset retry_session retry_upload retry_job retry_state attempt_after_stop

    [[ -f "$fixture" ]] || phase13_die "Media fixture is missing: $fixture"

    phase13_pause "2.1 Create a run-owned asset for the successful upload"
    phase13_create_demo_folder upload
    happy_asset="$(phase13_create_metadata upload "$RUN_ID upload happy path")"
    phase13_log "Created happy-path asset: $happy_asset"
    phase13_case_pass

    phase13_pause "2.2 Declare checksum and size, then create a presigned upload session"
    phase13_create_upload_session create-happy-session "$happy_asset" "$fixture" small-64x64.png
    happy_session="$PHASE13_LAST_JSON"
    happy_upload="$(jq -r '.data.uploadId' <<<"$happy_session")"
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
    phase13_capture_service_logs upload "$ACCESS_CORE_CONTAINER" "$ASSET_CORE_CONTAINER" "$MEDIA_WORKER_CONTAINER" "$MINIO_CONTAINER"
    phase13_log "Retry path recovered the same job on attempt 2 with no recommit and a single promotion."
    phase13_log "Run-owned metadata and object evidence is intentionally preserved for inspection."
}
