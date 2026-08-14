# Local MinIO

Local development only. One private bucket over HTTP, bound to localhost, with
development credentials. Staging and production need a supported MinIO
distribution, HTTPS on both the internal and public endpoints, and credentials
issued outside this repository.

## Why CORS is not a bucket policy here

The obvious approach — an S3 `CORSConfiguration` applied with `mc cors set` —
does not work. MinIO does not implement the S3 `PutBucketCors` API and returns:

```text
mc: <ERROR> Unable to set bucket CORS configuration for local/seta-media.
A header you provided implies functionality that is not implemented.
```

Browser CORS is **server-level** configuration instead, set on the `minio`
service in `docker-compose.yml`:

```yaml
MINIO_API_CORS_ALLOW_ORIGIN: http://localhost:3000,http://localhost:4000
```

Consequences worth knowing before writing the browser upload path:

- The allowlist is per server, not per bucket. Every bucket on this instance
  shares it.
- MinIO permits the standard request headers a signed PUT carries — including
  `Content-Type`, `If-None-Match`, and `x-amz-checksum-sha256` — so there is no
  per-header allowlist to maintain locally. A provider that *does* implement
  bucket CORS will need those three named explicitly.
- Leaving `MINIO_API_CORS_ALLOW_ORIGIN` unset defaults to `*`. That is why it is
  set explicitly even for local development.

## Files

| File | Purpose |
|---|---|
| `init.sh` | Idempotent bucket creation, private policy, and a private-policy assertion that fails the stack rather than proceeding |
