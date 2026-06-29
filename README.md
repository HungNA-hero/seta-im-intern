# seta-im-intern

## Phase 1 local database baseline

This baseline follows the M1 architecture:

- Node hosts the public Fastify + GraphQL Yoga API and owns `access-db`.
- Go Asset Core is an internal REST/HTTP JSON service and owns `asset-db`.
- Services communicate through APIs/contracts, not cross-database joins or foreign keys.

### Prerequisites

- Node.js (v20+ recommended)
- Go (1.21+ recommended)
- Docker Desktop
- Docker Compose v2

### Quick Start (NPM Scripts)

We have provided NPM scripts at the root level to simplify running and managing the system. 
Before starting, run the following to install all dependencies:
```bash
npm ci
npm --prefix services/access-core ci
```

For a clean environment, start the databases and apply all Flyway migrations before starting the application services:

```bash
npm run docker:up
npm run docker:migrate
```

- **Start both application services** (databases must already be migrated):
  ```bash
  npm run dev:all
  ```
- **Start databases only**: `npm run docker:up`
- **Run migrations**: `npm run docker:migrate`
- **Stop databases**: `npm run shut:all`
- **Clean databases (remove volumes)**: `npm run clean:all`
- **View logs**: `npm run docker:logs`
- **Run individual services**:
  - `npm run dev:access` (starts access-core)
  - `npm run dev:asset` (starts asset-core)

---

### Manual Setup & Commands

If you prefer to run commands manually, here are the underlying equivalents.

#### Start databases

```bash
docker compose -f infra/docker-compose.yml up -d asset-db access-db
```

Asset DB is exposed on `localhost:5433` by default.
Access DB is exposed on `localhost:5434` by default.

#### Run migrations

```bash
docker compose -f infra/docker-compose.yml --profile migration run --rm flyway-asset
docker compose -f infra/docker-compose.yml --profile migration run --rm flyway-access
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

#### Inspect databases

```bash
docker compose -f infra/docker-compose.yml exec asset-db psql -U asset_user -d asset_db
docker compose -f infra/docker-compose.yml exec access-db psql -U access_user -d access_db
```

Useful checks:

```sql
\dt
SELECT * FROM flyway_schema_history ORDER BY installed_rank;
SELECT id, org_id, path, name FROM folders ORDER BY path;
SELECT title, external_source, external_id FROM metadata_items;
```

#### Run Go Asset Core locally

From the root directory:

```bash
cd services/asset-core
go run ./cmd/server/main.go
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

#### Reset local data

```bash
docker compose -f infra/docker-compose.yml down -v
docker compose -f infra/docker-compose.yml up -d asset-db access-db
docker compose -f infra/docker-compose.yml --profile migration run --rm flyway-asset
docker compose -f infra/docker-compose.yml --profile migration run --rm flyway-access
```

### Environment overrides

Docker Compose uses safe local defaults. If a local override is needed, set the variables in your shell or in an untracked `.env` file.
