#!/bin/sh
# Idempotent local bucket initialization. Safe to re-run: every step either
# creates something absent or asserts what already exists.
#
# Local development only. Any environment beyond a developer machine needs a
# supported MinIO distribution, TLS endpoints, and credentials that are not
# these.
#
# CORS is deliberately not set here. MinIO does not implement the S3
# PutBucketCors API — `mc cors set` returns NotImplemented — so browser CORS is
# server-level configuration via MINIO_API_CORS_ALLOW_ORIGIN on the minio
# service. See README.md in this directory.
set -eu

ALIAS=local
BUCKET="${MEDIA_BUCKET:-seta-media}"
ENDPOINT="${MINIO_INTERNAL_ENDPOINT:-http://minio:9000}"

mc alias set "$ALIAS" "$ENDPOINT" "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null

mc mb --ignore-existing "$ALIAS/$BUCKET"

# The bucket must never serve anonymous reads. Raw originals live here, and the
# only legitimate read path is a short-lived signed URL for a processed output.
mc anonymous set none "$ALIAS/$BUCKET"

# Assert it rather than trusting the set above, so a policy that drifts stops
# the stack instead of silently exposing originals.
policy=$(mc anonymous get "$ALIAS/$BUCKET")
case "$policy" in
    *'`none`'* | *'`private`'*) ;;
    *)
        echo "bucket $BUCKET is not private: $policy" >&2
        exit 1
        ;;
esac

echo "minio ready: bucket=$BUCKET private=yes"
