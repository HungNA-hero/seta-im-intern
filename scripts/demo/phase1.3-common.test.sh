#!/usr/bin/env bash
set -Eeuo pipefail

TEST_TEMP="$(mktemp -d /tmp/phase13-evidence-test.XXXXXX)"
trap 'rm -rf -- "$TEST_TEMP"' EXIT

export PHASE13_RUN_ID="evidence-test"
export PHASE13_LOG_ROOT="$TEST_TEMP/evidence"
export PHASE13_AUTO_CONTINUE=1

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=phase1.3-common.sh
source "$SCRIPT_DIR/phase1.3-common.sh"

phase13_initialize_evidence

phase13_case_service_containers() { printf '%s\n' fake-service; }
docker() {
    if [[ "${1:-}" == "logs" ]]; then
        printf '%s\n' '{"url":"http://minio.test/object?X-Amz-Signature=unsafe","token":"unsafe","msg":"processed"}'
        return 0
    fi
    return 1
}

phase13_pause "1.1 Render one complete evidence block"
large_value="$(printf '%02000d' 0)"
phase13_record_json soft-delete api-responses sample-api \
  "$(jq -cn --arg url 'http://minio.test/object?X-Amz-Signature=unsafe' --arg token unsafe --arg detail "$large_value" \
    '{httpStatus:200,url:$url,token:$token,detail:$detail}')"
phase13_record_json soft-delete database-snapshots sample-database \
  '{"rows":["durable=true"]}'
pass_output="$TEST_TEMP/pass.out"
phase13_case_pass >"$pass_output"

grep -Fq 'Evidence 1.1' "$pass_output"
grep -Fq 'API evidence' "$pass_output"
grep -Fq 'Database evidence' "$pass_output"
grep -Fq 'Service logs (new, bounded)' "$pass_output"
grep -Fq 'PASS 1.1' "$pass_output"
grep -Fq '?REDACTED' "$pass_output"
! grep -Fq 'X-Amz-Signature' "$pass_output"
! grep -Fq 'unsafe' "$pass_output"
awk 'length($0) > 950 { exit 1 }' "$pass_output"
awk '
  /API evidence/ { api=NR }
  /Database evidence/ { database=NR }
  /Service logs \(new, bounded\)/ { logs=NR }
  /PASS 1.1/ { pass=NR }
  END { exit !(api < database && database < logs && logs < pass) }
' "$pass_output"
! grep -Eq 'X-Amz-Signature|"token":"unsafe"' \
  "$PHASE13_LOG_ROOT/soft-delete/service-logs/fake-service.log"

phase13_pause "1.2 Omit empty record categories"
empty_output="$TEST_TEMP/empty.out"
phase13_case_pass >"$empty_output"
! grep -Fq 'API evidence' "$empty_output"
! grep -Fq 'Database evidence' "$empty_output"
grep -Fq 'PASS 1.2' "$empty_output"

phase13_pause "1.3 Render a failed case"
PHASE13_FAILURE_MESSAGE="controlled failure"
fail_output="$TEST_TEMP/fail.out"
phase13_case_fail >"$fail_output"
grep -Fq 'FAIL 1.3: controlled failure' "$fail_output"
! grep -Fq 'PASS 1.3' "$fail_output"

printf 'phase1.3 evidence renderer checks passed\n'
