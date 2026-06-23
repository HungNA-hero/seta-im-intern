# seta-im-intern

## Phase 1 local database baseline

This baseline follows the M1 architecture:

- Go GraphQL Asset Core owns `asset-db`.
- Node Access Policy Service owns `access-db`.
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

- `user_ref`
- `folders`
- `metadata_items`
- internal Asset DB foreign keys and indexes
- demo `Root / Animals / Dogs` seed data

`flyway-access` creates only the Access DB Flyway baseline. RBAC and object permission tables are handled by the KAN-19 task.

### Inspect databases

```bash
docker compose exec asset-db psql -U asset_user -d asset_db
docker compose exec access-db psql -U access_user -d access_db
```

Useful checks:

```sql
\dt
SELECT * FROM flyway_schema_history ORDER BY installed_rank;
SELECT id, parent_id, name FROM folders ORDER BY created_at;
SELECT title, external_source, external_id FROM metadata_items;
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
