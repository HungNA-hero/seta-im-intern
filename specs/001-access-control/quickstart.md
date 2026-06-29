# Quickstart & Validation Guide: Access Control Service

This guide walks through starting the service and verifying every major feature works
end-to-end using GraphiQL. Run each step in order.

---

## Prerequisites

1. Docker running locally.
2. Repo cloned at `/home/dungpd/Downloads/seta-im-intern` (or your path).
3. `services/access-core` dependencies installed: `cd services/access-core && npm install`.
4. Prisma client generated: `npx prisma generate` (run after `prisma/schema.prisma` exists).

---

## 1. Start the Database and Apply Migrations

```bash
# From repo root
docker compose -f infra/docker-compose.yml up -d access-db

docker compose -f infra/docker-compose.yml --profile migration run --rm flyway-access
```

Verify seed data applied (should show 4 permission_actions rows):

```bash
docker compose -f infra/docker-compose.yml exec access-db \
  psql -U access_user -d access_db \
  -c "SELECT code FROM access.permission_actions ORDER BY code;"
```

Expected output:
```
       code
-------------------
 delete
 manage_permissions
 read
 write
```

---

## 2. Start the Dev Server

```bash
cd services/access-core
npm run dev
# Server starts on http://localhost:4000
```

Health check:
```bash
curl http://localhost:4000/health
# Expected: {"status":"ok"}
```

Open GraphiQL: `http://localhost:4000/graphql`

---

## 3. Verify Read Queries (Existing Skeleton)

```graphql
# List seeded users
{ users { id email displayName isActive } }
```

Expected: at least `admin@seta.com` and `dungpd@seta.com`.

```graphql
# List roles for seeded org
{ roles(orgId: "c0000000-0000-0000-0000-000000000001") { id code name } }
```

Expected: `org_admin` and `viewer` roles.

---

## 4. User & Organization Mutations

```graphql
# Create a test user
mutation {
  createUser(email: "test@seta.com", displayName: "Test User") {
    id email isActive
  }
}
```

Save the returned `id` as `<USER_ID>`.

```graphql
# Add to seeded org
mutation {
  addOrgMember(
    orgId: "c0000000-0000-0000-0000-000000000001"
    userId: "<USER_ID>"
  )
}
```

Expected: `true`.

---

## 5. Role Assignment

```graphql
# Assign the viewer role
mutation {
  assignRole(
    orgId:  "c0000000-0000-0000-0000-000000000001"
    userId: "<USER_ID>"
    roleId: "b0000000-0000-0000-0000-000000000002"
  )
}
```

Expected: `true`.

---

## 6. canDo — Denied (No OLP Grant Yet)

```graphql
{
  canDo(
    userId:       "<USER_ID>"
    action:       read
    resourceType: folder
    resourceId:   "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
  ) { allowed reason }
}
```

Expected: `{ "allowed": false, "reason": "no object permission" }`

(RBAC ceiling passes — viewer role has read/folder — but no OLP grant exists yet.)

---

## 7. Grant Object-Level Permission

```graphql
mutation {
  grantObjectPermission(
    orgId:        "c0000000-0000-0000-0000-000000000001"
    resourceType: folder
    resourceId:   "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
    action:       read
    granteeUserId: "<USER_ID>"
    grantedBy:    "00000000-0000-0000-0000-000000000001"
  ) { id }
}
```

---

## 8. canDo — Allowed

```graphql
{
  canDo(
    userId:       "<USER_ID>"
    action:       read
    resourceType: folder
    resourceId:   "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
  ) { allowed reason }
}
```

Expected: `{ "allowed": true, "reason": null }`

---

## 9. Revoke Grant → Denied Again

```graphql
# Use the grant id returned in step 7
mutation { revokeObjectPermission(id: "<GRANT_ID>") }
```

Re-run the canDo from step 8 → Expected: `{ "allowed": false, "reason": "no object permission" }`

---

## 10. trainer_admin Override

```graphql
{
  canDo(
    userId:       "00000000-0000-0000-0000-000000000001"
    action:       delete
    resourceType: metadata_item
    resourceId:   "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
  ) { allowed reason }
}
```

Expected: `{ "allowed": true, "reason": "trainer_admin" }`
(admin@seta.com holds org_admin which has `trainer_admin` behaviour — verify against seed.)

---

## 11. Deactivation Cascade

```graphql
mutation { deactivateUser(id: "<USER_ID>") { id isActive } }
```

Expected: `{ "isActive": false }`.

Verify roles removed:
```graphql
{ user(id: "<USER_ID>") { isActive } }
```

canDo after deactivation (any action) → Expected: `{ "allowed": false, "reason": "user not found" }`
(or "no RBAC ceiling" if user lookup still returns row — validate against FR-010 step 1.)

---

## 12. Error Cases

```graphql
# Duplicate grant (re-run step 7 mutation) → expect CONFLICT error
# Both granteeUserId and granteeRoleId set → expect BAD_INPUT error
mutation {
  grantObjectPermission(
    orgId: "c0000000-0000-0000-0000-000000000001"
    resourceType: folder
    resourceId:   "cccccccc-cccc-cccc-cccc-cccccccccccc"
    action: read
    granteeUserId: "00000000-0000-0000-0000-000000000001"
    granteeRoleId: "b0000000-0000-0000-0000-000000000001"
    grantedBy:     "00000000-0000-0000-0000-000000000001"
  ) { id }
}
```

Expected response: `errors[0].extensions.code = "BAD_INPUT"`.

---

## Seeded Reference IDs

| Entity | ID |
|---|---|
| Org (seta) | `c0000000-0000-0000-0000-000000000001` |
| User (admin@seta.com) | `00000000-0000-0000-0000-000000000001` |
| User (dungpd@seta.com) | `00000000-0000-0000-0000-000000000002` |
| Role (org_admin) | `b0000000-0000-0000-0000-000000000001` |
| Role (viewer) | `b0000000-0000-0000-0000-000000000002` |
