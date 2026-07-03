# RBAC and OLP Integration Matrix

This document defines the KAN-41 integration contract exercised by `src/__tests__/policyMatrixE2E.e2e.test.ts`.

## Test boundary

The matrix runs through Fastify/Yoga GraphQL, the real Access Core policy implementation, Prisma, the disposable Access database, Go Asset Core, and the disposable Asset database. It does not mock `canDo`, Prisma, the Asset client, or GraphQL context.

Run it with the repository E2E harness:

```powershell
cd ../..
./scripts/run_e2e.ps1
```

The harness creates empty PostgreSQL 16 databases, applies Asset and Access Flyway migrations, starts Go Asset Core, runs every `*.e2e.test.ts` file, and removes the disposable resources.

## Actors and policy modes

- Seta administrator: seeded `org_admin`; allowed inside Seta without a direct object grant.
- Seta viewer: seeded read-only role.
- Non-member: active user without Seta membership.
- OLP disabled: role permissions are authoritative; direct object grants do not expand the RBAC ceiling.
- OLP enabled: ordinary members require a matching org/resource/action object grant; administrator overrides remain valid.

## Matrix

| ID    | Scenario                                                  | Expected decision                      | Side-effect invariant                            |
| ----- | --------------------------------------------------------- | -------------------------------------- | ------------------------------------------------ |
| PM-01 | Non-member updates a folder                               | `FORBIDDEN: not a member`              | Zero Asset call; folder unchanged                |
| PM-02 | Org admin writes with OLP disabled and enabled            | Allowed in both modes                  | One Asset mutation per write                     |
| PM-03 | Viewer reads with OLP disabled                            | Allowed by RBAC                        | One Asset read                                   |
| PM-04 | Viewer writes with OLP disabled, even with a direct grant | `FORBIDDEN: no RBAC ceiling`           | Zero Asset call; folder unchanged                |
| PM-05 | Viewer writes with OLP enabled and no grant               | `FORBIDDEN: no object permission`      | Zero Asset call; folder unchanged                |
| PM-06 | Viewer writes with an exact OLP grant                     | Allowed                                | One Asset mutation; target changes               |
| PM-07 | Admin grants viewer write access                          | Deny before grant, allow after grant   | Grant audit fields are requester-derived         |
| PM-08 | Admin revokes viewer write access                         | Allow before revoke, deny after revoke | Grant removed; denied write has zero Asset calls |
| PM-09 | Grants use the wrong resource or organization             | `FORBIDDEN: no object permission`      | Zero Asset call; folder unchanged                |

## Fixture invariants

- Every test recreates one deterministic Asset folder.
- Every test removes direct permission fixtures and restores Seta to `olp_enabled=false` afterward, including failure paths.
- PM-07 and PM-08 use public GraphQL grant/revoke mutations; direct Prisma writes are limited to non-mutation fixture scenarios.
- Tests assert policy reason, GraphQL code, Asset call count, and persisted state instead of relying on HTTP 200 alone.

## Architecture references

- Public GraphQL and policy decisions belong to Node Access Core.
- Access Core reads only the Access database through Prisma.
- Folder persistence belongs to Go Asset Core and the Asset database.
- Object permission `resource_id` is a logical reference; there is no cross-database foreign key.
