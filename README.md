# seta-im-intern

## Phase 1 local database baseline

This baseline follows the M1 architecture:

- Node hosts the public Fastify + GraphQL Yoga API and owns `access-db`.
- Go Asset Core is an internal REST/HTTP JSON service and owns `asset-db`.
- Services communicate through APIs/contracts, not cross-database joins or foreign keys.

### Prerequisites

- Docker Desktop
- Docker Compose v2

### Start databases

```bash
docker compose up -d asset-db access-db
```

Asset DB is exposed on `localhost:5433` by default.
Access DB is exposed on `localhost:5434` by default.

### Run migrations

```bash
docker compose --profile migration run --rm flyway-asset
docker compose --profile migration run --rm flyway-access
```

`flyway-asset` creates the Asset DB schema from the Go DBML:

- `organization_ref`
- `user_ref`
- `folders`
- `metadata_items`
- internal Asset DB foreign keys, ltree indexes, and sibling folder name uniqueness
- demo `Root / Animals / Dogs` seed data

`flyway-access` creates the Access DB baseline:

- `users`
- `organizations` with the `olp_enabled` feature flag
- `organization_members`
- organization-scoped `roles` and `user_roles`
- `permission_actions` and `role_permissions`
- unified `object_permissions` for folder and metadata grants

### Inspect databases

```bash
docker compose exec asset-db psql -U asset_user -d asset_db
docker compose exec access-db psql -U access_user -d access_db
```

Useful checks:

```sql
\dt
SELECT * FROM flyway_schema_history ORDER BY installed_rank;
SELECT id, org_id, path, name FROM folders ORDER BY path;
SELECT title, external_source, external_id FROM metadata_items;
```

### Run Go Asset Core locally

From `go-asset-core`:

```bash
go run ./cmd/server
```

The server reads Asset DB settings from environment variables and uses Docker Compose-compatible defaults:

- `ASSET_DB_HOST=localhost`
- `ASSET_DB_PORT=5433`
- `ASSET_DB_NAME=asset_db`
- `ASSET_DB_USER=asset_user`
- `ASSET_DB_PASSWORD=asset_password`
- `ASSET_DB_SSLMODE=disable`

Useful checks:

```bash
curl http://localhost:8080/healthz
curl "http://localhost:8080/internal/api/v1/folders?orgId=00000000-0000-0000-0000-000000000010&rootPath=root"
```

### Reset local data

```bash
docker compose down -v
docker compose up -d asset-db access-db
docker compose --profile migration run --rm flyway-asset
docker compose --profile migration run --rm flyway-access
```

### Environment overrides

Docker Compose uses safe local defaults. If a local override is needed, set the variables in your shell or in an untracked `.env` file.
