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

`flyway-access` creates the Access DB baseline:
- `users`
- `organizations` with the `olp_enabled` feature flag
- `organization_members`
- organization-scoped `roles` and `user_roles`
- `permission_actions` (the only reference data the application depends on) and `role_permissions`
- unified `object_permissions` for folder and metadata grants

#### Demo/seed data (optional)

Migrations only seed the `permission_actions` reference rows the application needs to
function — no demo organization, users, or content. The demo `seta` org, `admin@seta.com`/
`dungpd@seta.com` users, and `Root / Animals / Dogs` folder tree used by
`scripts/trainer-demo.sh` and `scripts/sprint4-demo.sh` live in standalone seed scripts,
applied manually after migrating:

```bash
docker exec -i seta-access-db psql -U access_user -d access_db < infra/db/access/seed/demo_fixtures.sql
docker exec -i seta-asset-db psql -U asset_user -d asset_db < infra/db/asset/seed/demo_fixtures.sql
```

Both demo scripts apply these automatically as part of their own database bootstrap step.

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

---

## Service boundaries

```
services/
  asset-core/     Go 1.26 · stdlib net/http · GORM · REST/JSON        (port 8080)
  access-core/    Node 20 · TypeScript 5 · Fastify 4 · GraphQL Yoga 5 · Prisma 7  (port 4000)
```

- **asset-core** is an internal, non-public REST service. It owns folders and metadata
  items — the actual asset tree. It has no concept of roles or permissions; it defers
  every access decision to access-core.
- **access-core** is the public-facing GraphQL API. It owns users, organizations, roles,
  RBAC ceilings, and object-level permission grants, and is the sole authorization
  decision point (`canDo`).
- **No shared database, no cross-service joins or foreign keys.** The two services talk
  only over HTTP:
  - asset-core calls access-core's `canDo` GraphQL query to authorize an operation before
    performing it.
  - access-core calls asset-core's internal REST API when it needs asset facts to make a
    decision (e.g. a folder's `path`, to walk the ancestor chain for inherited grants).
- asset-core trusts a user-id header forwarded by the gateway/caller — it does not
  authenticate anyone itself (see [Known limitations](#known-limitations)).

Layering inside each service:

- **asset-core**: `internal/domain` (interfaces + GORM models) → `internal/repository`
  (GORM impls) → `internal/usecase` (business logic) → `internal/delivery/http`
  (`http.ServeMux` handlers) → `cmd/server/main.go` (wiring).
- **access-core**: `db/pool.ts` (`pg.Pool`) + `db/prisma.ts` (Prisma singleton) →
  `db/queries/*.ts` (one file per domain) → `graphql/resolvers/*.ts` → `graphql/typeDefs.ts`
  (SDL) → `server.ts` (Fastify + Yoga at `/graphql`, `/health`).

## DB ownership

Two PostgreSQL 16 databases, strictly partitioned by owning service:

| Database | Host port | Owner | Schema |
|---|---|---|---|
| `asset_db` | 5433 | asset-core | `public` |
| `access_db` | 5434 | access-core | `access` |

**`asset_db`**:
- `organization_ref`, `user_ref` — shadow UUIDs mirroring access-db's `organizations`/`users`
  IDs, purely so asset-db can enforce its own FK constraints without a cross-DB join.
- `folders` — `ltree` path, self-referential tree, soft-delete via `deleted_at`, unique
  sibling names enforced by a partial index on active (non-deleted) rows.
- `metadata_items` — text-only image metadata, soft-delete, FK to `folders`.
- `updated_at` on both tables is auto-maintained by a trigger.

**`access_db`** (all tables in the `access` schema) — full RBAC + OLP model:
- `users`, `organizations` (with `olp_enabled` flag), `organization_members`
- `roles`, `user_roles` — RBAC ceiling assignment
- `permission_actions`, `role_permissions` — what a role may do per resource type
- `object_permissions` — single unified table for both folder and metadata-item grants

Migrations live in `infra/db/{asset,access}/migrations/V{n}__{description}.sql`. Schema
DDL and seed data are always in separate files when both exist (`access_db`'s `V2__` seeds
only the `permission_actions` reference rows the application depends on); seed rows use
fixed, deterministic UUIDs with `ON CONFLICT DO NOTHING` for idempotent re-runs. Demo
fixture data (org/users/roles/folder tree) is not part of any migration — see "Demo/seed
data (optional)" above. `access_db` migrations prefix every table reference with `access.`.

## Permission model

Authorization is entirely owned by access-core's `canDo(userId, action, resourceType,
resourceId, orgId)` resolver, called internally by asset-core (and by access-core's own
resolvers) before every mutation or read. It always returns `{ allowed, reason }`; a
`false` result surfaces as a GraphQL `FORBIDDEN` error.

**Actions**: `read`, `write`, `delete`, `manage_permissions`
**Resource types**: `folder`, `metadata_item`

### Two mutually exclusive modes, per organization

`organizations.olp_enabled` selects the mode for that org — **RBAC mode and OLP mode are
not ANDed together**, one fully replaces the other for regular users:

| Mode | Source of truth | Object grants queried? |
|---|---|---|
| `olp_enabled = false` (RBAC) | `role_permissions` ceiling only | No — never queried, even if a grant exists |
| `olp_enabled = true` (OLP) | `object_permissions` grant only | Yes — RBAC ceiling is ignored entirely |

So in RBAC mode, a direct object grant that exceeds the user's role ceiling has **no
effect** — the ceiling wins. In OLP mode, the role ceiling has no effect — only explicit
grants matter.

### Bypasses (checked before mode logic)

1. **`trainer_admin`** — global override, allowed on anything in any org, no queries.
   Developer/test convenience only: the bypass is automatically inert whenever the service
   runs with `NODE_ENV=production`.
2. **`org_admin`** — per-org bypass, full access to everything in that org, no OLP check.
3. **Root sentinel** (`resourceId === orgId`, used to authorize creating a top-level
   folder) — always decided by RBAC ceiling in both modes, since there's no real folder
   row to hold an object grant.

### Inheritance (OLP mode only)

A grant on a folder propagates to all descendant folders (via the `ltree` ancestor chain)
and to metadata items filed anywhere under that subtree, for `read`/`write`/`delete`.
**`manage_permissions` never inherits** — it only applies to the exact resource it was
granted on. RBAC mode has no concept of inheritance since it never looks at grants at all.

### Creator has no implicit access

`created_by` on `folders`/`metadata_items` records who made the resource but confers
**no** automatic permission — there's no `owner` column and no bypass derived from it.
Creators go through the same RBAC/OLP/grant checks as anyone else.

### Soft-delete and grants

Soft-deleting a folder or metadata item leaves its `object_permissions` rows in place.
The grants are inert only because the resource is no longer readable through any path; if
the resource is restored, prior access comes back without re-granting.

## Demo users & seed data

Not seeded by migrations — apply `infra/db/access/seed/demo_fixtures.sql` and
`infra/db/asset/seed/demo_fixtures.sql` manually (see "Demo/seed data (optional)" above).

**Organization**: `seta` (`00000000-0000-0000-0000-000000000010`), `olp_enabled = false`
(RBAC mode) by default.

**Users**:

| Email | Display name | Role | ID |
|---|---|---|---|
| `admin@seta.com` | Seta Admin | `org_admin` | `...0001` |
| `dungpd@seta.com` | Dung Pham Duc | `viewer`, `trainer_admin` | `...0002` |

- `org_admin`: RBAC ceiling = all 4 actions on both `folder` and `metadata_item`.
- `viewer`: RBAC ceiling = `read` only on both resource types.
- `trainer_admin`: global bypass, seeded on `dungpd@seta.com` for local testing only — inert
  whenever the service runs with `NODE_ENV=production`.

**Demo asset tree** (`asset_db`, org `seta`): `Root` → `Animals` → `Dogs`, plus a sample
metadata item, so folder-tree and metadata queries have something to return out of the
box.

## Known limitations

- **No authentication.** Both services trust an already-authenticated user ID passed in
  (asset-core via a header from its caller, access-core's `canDo` via its arguments).
  Verifying *who* a user is is out of scope for this project — only authorization is
  implemented.
- **`canDo` is internal-only** and must not be exposed outside the trusted service
  network; it is not access-controlled itself beyond that assumption.
- **One org per user context per request.** A user can belong to multiple orgs, but every
  permission decision is scoped to a single `orgId` passed in — there's no cross-org
  query.
- **Only two resource types** (`folder`, `metadata_item`) and a fixed, global set of four
  actions. Organizations cannot define custom actions or resource types.
- **`trainer_admin`/`org_admin` are reserved role codes** — orgs cannot repurpose them for
  regular roles.
- **No self-service registration** — users, orgs, and role assignments are all created by
  an admin through the API; there's no signup flow.
- **RBAC mode ignores object grants outright**, even if one exists (it's never queried).
  This is intentional but easy to misread as a bug when testing: granting a viewer
  `write` on a specific folder does nothing until the org's `olp_enabled` flag is turned
  on.
- **Soft-deleted resources keep their grants** rather than cascading a permission
  cleanup — by design, but means `object_permissions` can accumulate rows pointing at
  dead resources.
- Scope is sized for an intern training project: single-tenant defaults, ~100 users,
  ~10 orgs — not load-tested beyond that.
