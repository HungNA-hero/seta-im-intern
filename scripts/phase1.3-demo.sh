#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=demo/phase1.3-common.sh
source "$SCRIPT_DIR/demo/phase1.3-common.sh"
# shellcheck source=demo/phase1.3-soft-delete-cleanup.sh
source "$SCRIPT_DIR/demo/phase1.3-soft-delete-cleanup.sh"
# shellcheck source=demo/phase1.3-media-upload.sh
source "$SCRIPT_DIR/demo/phase1.3-media-upload.sh"

usage() {
    printf 'Usage: %s {soft-delete|upload|all}\n' "$0"
    printf 'Runs an interactive, evidence-capturing demo against the existing Compose environment.\n'
}

MODE="${1:-all}"
if [[ "$MODE" != "soft-delete" && "$MODE" != "upload" && "$MODE" != "all" ]] || (( $# > 1 )); then
    usage >&2
    exit 2
fi

phase13_initialize_evidence
exec > >(tee >(phase13_strip_ansi >>"$TRANSCRIPT_LOG")) 2>&1
trap 'code=$?; if (( code != 0 )); then phase13_case_fail; phase13_run_summary FAIL; fi; phase13_finish "$code"; exit "$code"' EXIT
trap 'PHASE13_FAILURE_MESSAGE="Interrupted"; exit 130' INT TERM

phase13_run_banner "$MODE"

phase13_pause "Preflight: verify the existing environment without resetting it"
phase13_preflight
phase13_log "Preflight passed. Existing volumes and unrelated data are preserved."

case "$MODE" in
    soft-delete) phase13_run_soft_delete_cleanup ;;
    upload) phase13_run_media_upload ;;
    all)
        phase13_run_soft_delete_cleanup
        phase13_run_media_upload
        ;;
esac

phase13_run_summary PASS
