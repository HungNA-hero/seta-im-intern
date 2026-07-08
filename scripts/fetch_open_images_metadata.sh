#!/usr/bin/env bash
# Fetches and prepares Open Images V7 metadata for import (Linux/macOS port of
# fetch_open_images_metadata.ps1). Orchestration wrapper around the Go
# prepare-open-images command; also supports --verify-only, mirroring the
# PowerShell VerifyOnly manifest/checksum audit used by the FD-00/FD-05
# Final Demo Runbook steps.
#
# Usage:
#   ./fetch_open_images_metadata.sh [--split validation] [--max-items 25] \
#       [--output-dir DIR] [--verify-only]
set -euo pipefail

SPLIT="validation"
MAX_ITEMS=25
OUTPUT_DIR="${TMPDIR:-/tmp}/seta-open-images-v7"
VERIFY_ONLY=0

while [[ $# -gt 0 ]]; do
    case "$1" in
        --split) SPLIT="$2"; shift 2 ;;
        --max-items) MAX_ITEMS="$2"; shift 2 ;;
        --output-dir) OUTPUT_DIR="$2"; shift 2 ;;
        --verify-only) VERIFY_ONLY=1; shift ;;
        *) echo "Unknown argument: $1" >&2; exit 1 ;;
    esac
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
ASSET_CORE_DIR="$WORKSPACE_ROOT/services/asset-core"

if [[ ! -d "$ASSET_CORE_DIR" ]]; then
    echo "services/asset-core directory not found." >&2
    exit 1
fi

if [[ "$VERIFY_ONLY" -eq 1 ]]; then
    MANIFEST_PATH="$OUTPUT_DIR/provenance-manifest.json"
    if [[ ! -f "$MANIFEST_PATH" ]]; then
        echo "Manifest not found in $OUTPUT_DIR for verification." >&2
        exit 1
    fi
    echo "VerifyOnly: Reading manifest..."

    # filename -> "url|sha256"
    declare -A EXPECTED_ARTIFACTS=(
        ["oidv7-val-annotations-human-imagelabels.csv"]="https://storage.googleapis.com/openimages/v7/oidv7-val-annotations-human-imagelabels.csv|92ddbdfceb3626e044df5e89100b24f6c22a79c1888a4bddd00a6f231d86d56a"
        ["oidv7-class-descriptions.csv"]="https://storage.googleapis.com/openimages/v7/oidv7-class-descriptions.csv|84a4373a0efb7fd6d93fe19b0e7ceb6c1b855c233d13b9b78a9a33655c9fdce3"
        ["validation-images-with-rotation.csv"]="https://storage.googleapis.com/openimages/2018_04/validation/validation-images-with-rotation.csv|ed93a0e121fe345effdfc7359b848dbc64a1ff6778c8c73563157cb500b33a17"
    )

    tool_version=$(jq -r '.tool_version' "$MANIFEST_PATH")
    if [[ "$tool_version" != "v1.0.0" ]]; then
        echo "Invalid manifest version" >&2
        exit 1
    fi

    artifact_count=$(jq '.artifacts | length' "$MANIFEST_PATH")
    if [[ "$artifact_count" -ne 3 ]]; then
        echo "Expected exactly 3 artifacts" >&2
        exit 1
    fi

    output_ids_count=$(jq -r '.output_ids_count' "$MANIFEST_PATH")
    output_ids_len=$(jq '.output_ids | length' "$MANIFEST_PATH")
    if [[ "$output_ids_count" -ne 25 || "$output_ids_len" -ne 25 ]]; then
        echo "Expected exactly 25 output IDs" >&2
        exit 1
    fi

    unique_sorted_count=$(jq '.output_ids | unique | length' "$MANIFEST_PATH")
    if [[ "$unique_sorted_count" -ne 25 ]]; then
        echo "Output IDs must be unique" >&2
        exit 1
    fi
    is_sorted=$(jq -r '.output_ids == (.output_ids | sort)' "$MANIFEST_PATH")
    if [[ "$is_sorted" != "true" ]]; then
        echo "Output IDs must be sorted" >&2
        exit 1
    fi

    declare -A SEEN_ARTIFACTS=()
    while IFS=$'\t' read -r filename source_url resolved_url sha_256 bytes; do
        if [[ -z "${EXPECTED_ARTIFACTS[$filename]+x}" ]]; then
            echo "Unexpected artifact filename: $filename" >&2
            exit 1
        fi
        if [[ -n "${SEEN_ARTIFACTS[$filename]+x}" ]]; then
            echo "Duplicate artifact filename: $filename" >&2
            exit 1
        fi
        SEEN_ARTIFACTS["$filename"]=1

        expected_url="${EXPECTED_ARTIFACTS[$filename]%%|*}"
        expected_sha="${EXPECTED_ARTIFACTS[$filename]##*|}"

        if [[ "$source_url" != "$expected_url" ]]; then
            echo "Unexpected source URL for $filename" >&2
            exit 1
        fi
        if [[ "$resolved_url" != https://storage.googleapis.com/* ]]; then
            echo "Resolved URL not in allowed host: $resolved_url" >&2
            exit 1
        fi
        if [[ "$sha_256" != "$expected_sha" ]]; then
            echo "Manifest checksum is not the trusted checksum for $filename" >&2
            exit 1
        fi

        artifact_path="$OUTPUT_DIR/$filename"
        if [[ ! -f "$artifact_path" ]]; then
            echo "Artifact not found: $filename" >&2
            exit 1
        fi
        actual_bytes=$(stat -c%s "$artifact_path")
        if [[ "$actual_bytes" -ne "$bytes" ]]; then
            echo "Artifact size mismatch for $filename" >&2
            exit 1
        fi
        actual_sha=$(sha256sum "$artifact_path" | awk '{print $1}')
        if [[ "$actual_sha" != "$expected_sha" ]]; then
            echo "Artifact checksum mismatch for $filename" >&2
            exit 1
        fi
    done < <(jq -r '.artifacts[] | [.filename, .source_url, .resolved_url, .sha_256, .bytes] | @tsv' "$MANIFEST_PATH")

    if [[ "${#SEEN_ARTIFACTS[@]}" -ne 3 ]]; then
        echo "Manifest does not contain the exact trusted artifact set" >&2
        exit 1
    fi

    if find "$OUTPUT_DIR" -type f \( -iname '*.jpg' -o -iname '*.jpeg' -o -iname '*.png' \
        -o -iname '*.gif' -o -iname '*.webp' -o -iname '*.bmp' -o -iname '*.partial' \) \
        -print -quit | grep -q .; then
        echo "Forbidden image binaries or partial files found in cache" >&2
        exit 1
    fi

    OUTPUT_PATH="$OUTPUT_DIR/validation-sample.json"
    if [[ ! -f "$OUTPUT_PATH" ]]; then
        echo "Output validation-sample.json not found." >&2
        exit 1
    fi

    output_hash=$(sha256sum "$OUTPUT_PATH" | awk '{print $1}')
    manifest_checksum=$(jq -r '.output_checksum' "$MANIFEST_PATH")
    if [[ "$output_hash" != "$manifest_checksum" ]]; then
        echo "Output checksum mismatch. Expected $manifest_checksum, got $output_hash" >&2
        exit 1
    fi

    version=$(jq -r '.version' "$OUTPUT_PATH")
    external_source=$(jq -r '.external_source' "$OUTPUT_PATH")
    if [[ "$version" != "1" || "$external_source" != "open_images_v7" ]]; then
        echo "Invalid validation sample contract" >&2
        exit 1
    fi
    metadata_count=$(jq '.metadata | length' "$OUTPUT_PATH")
    if [[ "$metadata_count" -ne 25 ]]; then
        echo "Validation sample must contain exactly 25 metadata records" >&2
        exit 1
    fi

    manifest_ids_sorted=$(jq -c '.output_ids | sort' "$MANIFEST_PATH")
    dataset_ids_sorted=$(jq -c '[.metadata[].external_id] | sort' "$OUTPUT_PATH")
    if [[ "$manifest_ids_sorted" != "$dataset_ids_sorted" ]]; then
        echo "Validation sample IDs do not match the manifest" >&2
        exit 1
    fi

    echo "VerifyOnly: Verification completed successfully."
    exit 0
fi

mkdir -p "$OUTPUT_DIR"

pushd "$ASSET_CORE_DIR" >/dev/null
echo "Running go prepare-open-images..."
go run ./cmd/prepare-open-images/main.go -split "$SPLIT" -max-items "$MAX_ITEMS" -output-dir "$OUTPUT_DIR"
echo "Open Images preparation completed successfully."
popd >/dev/null
