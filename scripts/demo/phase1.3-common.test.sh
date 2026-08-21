#!/usr/bin/env bash
set -Eeuo pipefail

TEST_TEMP="$(mktemp -d /tmp/phase13-evidence-test.XXXXXX)"
trap 'rm -rf -- "$TEST_TEMP"' EXIT

export PHASE13_RUN_ID="evidence-test"
export PHASE13_LOG_ROOT="$TEST_TEMP/evidence"
export PHASE13_AUTO_CONTINUE=1
export NO_COLOR=1
export PHASE13_ASCII=1

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=phase1.3-common.sh
source "$SCRIPT_DIR/phase1.3-common.sh"

phase13_initialize_evidence

phase13_case_service_containers() { printf '%s\n' fake-service; }
docker() {
    if [[ "${1:-}" == "logs" ]]; then
        printf '%s\n' \
          '{"time":1787297400000,"level":30,"url":"http://minio.test/object?X-Amz-Signature=unsafe","token":"unsafe","msg":"processed","jobId":"job-1"}' \
          'plain service line'
        return 0
    fi
    return 1
}

banner_output="$TEST_TEMP/banner.out"
phase13_run_banner all >"$banner_output"
grep -Fq 'PHASE 1.3 · Interactive demo' "$banner_output"
grep -Fq 'Run        evidence-test' "$banner_output"
grep -Fq 'Mode       all' "$banner_output"
[[ "$(printf '\033[31mERROR\033[0m\n' | phase13_strip_ansi)" == "ERROR" ]]

phase13_pause "1.1 Render one complete evidence block"
message_output="$TEST_TEMP/messages.out"
{
    phase13_log "Normal progress"
    phase13_warn "Controlled warning"
} >"$message_output"
grep -Fq '| INFO  Normal progress' "$message_output"
grep -Fq '| WARN  Controlled warning' "$message_output"
large_value="$(printf '%02000d' 0)"
phase13_record_json soft-delete api-responses sample-api \
  "$(jq -cn --arg url 'http://minio.test/object?X-Amz-Signature=unsafe' --arg token unsafe --arg detail "$large_value" \
    '{httpStatus:200,idempotencyReplayed:false,body:{data:{jobId:"job-1",status:"COMPLETED",url:$url,detail:$detail},token:$token}}')"
phase13_record_json soft-delete database-snapshots sample-database \
  '{"rows":["{\"durable\":true,\"attempts\":1}"]}'
pass_output="$TEST_TEMP/pass.out"
phase13_case_pass >"$pass_output"

grep -Fq '+- API' "$pass_output"
grep -Fq '| OK sample-api  HTTP 200' "$pass_output"
grep -Fq '"data": {' "$pass_output"
grep -Fq '"jobId": "job-1"' "$pass_output"
grep -Fq '"status": "COMPLETED"' "$pass_output"
! grep -Fq '"httpStatus"' "$pass_output"
grep -Fq '+- DATABASE' "$pass_output"
grep -Fq '"durable": true' "$pass_output"
! grep -Fq '+- SERVICES · new, bounded' "$pass_output"
! grep -Fq '"url"' "$pass_output"
! grep -Fq '"token"' "$pass_output"
grep -Eq '^\+- PASS 1\.1 · [0-9]+\.[0-9]{3}s$' "$pass_output"
! grep -Fq 'X-Amz-Signature' "$pass_output"
! grep -Fq 'unsafe' "$pass_output"
awk 'length($0) > 950 { exit 1 }' "$pass_output"
awk '
  /\+- API/ { api=NR }
  /\+- DATABASE/ { database=NR }
  /\+- PASS 1.1/ { pass=NR }
  END { exit !(api < database && database < pass) }
' "$pass_output"
! grep -Eq 'X-Amz-Signature|"token":"unsafe"' \
  "$PHASE13_LOG_ROOT/soft-delete/service-logs/fake-service.log"
jq -e 'keys == ["at","payload","step"] and .payload.httpStatus == 200' \
  "$PHASE13_LOG_ROOT/soft-delete/api-responses.jsonl" >/dev/null
jq -e 'keys == ["evidenceDirectory","finishedAt","message","runId","startedAt","status"] and .status == "RUNNING"' \
  "$PHASE13_LOG_ROOT/summary.json" >/dev/null

phase13_pause "1.2 Omit empty record categories"
empty_output="$TEST_TEMP/empty.out"
phase13_case_pass >"$empty_output"
! grep -Fq '+- API' "$empty_output"
! grep -Fq '+- DATABASE' "$empty_output"
grep -Eq '^\+- PASS 1\.2 · [0-9]+\.[0-9]{3}s$' "$empty_output"

phase13_pause "1.3 Render a failed case"
PHASE13_FAILURE_MESSAGE="controlled failure"
fail_output="$TEST_TEMP/fail.out"
phase13_case_fail >"$fail_output"
grep -Fq '+- SERVICES · new, bounded' "$fail_output"
grep -Fq '"level": 30' "$fail_output"
grep -Fq '"msg": "processed"' "$fail_output"
grep -Fq '|   plain service line' "$fail_output"
grep -Eq '^\+- FAIL 1\.3 · [0-9]+\.[0-9]{3}s · controlled failure$' "$fail_output"
! grep -Fq '+- PASS 1.3' "$fail_output"

malformed="$TEST_TEMP/malformed.jsonl"
printf '%s\n' 'not-json' >"$malformed"
set +e
phase13_print_case_records "$malformed" 1 API api >"$TEST_TEMP/malformed.out" 2>&1
malformed_status=$?
set -e
[[ "$malformed_status" == "1" ]]
grep -Fq 'ERROR Refusing to print malformed API record' "$TEST_TEMP/malformed.out"

printf 'phase1.3 evidence renderer checks passed\n'
