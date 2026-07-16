-- Demo fixture data for local development and the trainer/sprint demo scripts.
-- Not a Flyway migration: apply manually after `flyway migrate` in any environment
-- where you want the demo org/users/roles to exist.
--
-- Apply with:
--   docker exec -i seta-access-db psql -U access_user -d access_db < infra/db/access/seed/demo_fixtures.sql

-- Seed demo organization
INSERT INTO access.organizations (id, code, name, olp_enabled) VALUES
    ('00000000-0000-0000-0000-000000000010', 'seta', 'Seta', false)
ON CONFLICT (id) DO NOTHING;

-- Seed roles for Seta org (org_admin = global override; viewer = read-only ceiling;
-- trainer_admin = non-production-only global bypass, default-off unless explicitly
-- enabled with a future expiry, and always inert whenever NODE_ENV=production)
INSERT INTO access.roles (id, org_id, code, name, description) VALUES
    ('40000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000010',
     'org_admin', 'Org Admin', 'Full access to all resources and permissions within the org'),
    ('40000000-0000-0000-0000-000000000002', '00000000-0000-0000-0000-000000000010',
     'viewer',    'Viewer',    'Read-only access to all resources within the org'),
    ('40000000-0000-0000-0000-000000000003', '00000000-0000-0000-0000-000000000010',
     'trainer_admin', 'Trainer Admin', 'Non-production-only global bypass; default-off and expiry-bound')
ON CONFLICT (id) DO NOTHING;

-- Seed demo users
INSERT INTO access.users (id, email, display_name, is_active) VALUES
    ('00000000-0000-0000-0000-000000000001', 'admin@seta.com',  'Seta Admin',    true),
    ('00000000-0000-0000-0000-000000000002', 'dungpd@seta.com', 'Dung Pham Duc', true)
ON CONFLICT (id) DO NOTHING;

-- Add both users to Seta org
INSERT INTO access.organization_members (id, org_id, user_id) VALUES
    ('50000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000010', '00000000-0000-0000-0000-000000000001'),
    ('50000000-0000-0000-0000-000000000002', '00000000-0000-0000-0000-000000000010', '00000000-0000-0000-0000-000000000002')
ON CONFLICT (org_id, user_id) DO NOTHING;

-- Assign roles: admin → org_admin, dung → viewer + trainer_admin (for local bypass testing)
INSERT INTO access.user_roles (id, org_id, user_id, role_id) VALUES
    ('60000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000010',
     '00000000-0000-0000-0000-000000000001', '40000000-0000-0000-0000-000000000001'),
    ('60000000-0000-0000-0000-000000000002', '00000000-0000-0000-0000-000000000010',
     '00000000-0000-0000-0000-000000000002', '40000000-0000-0000-0000-000000000002'),
    ('60000000-0000-0000-0000-000000000003', '00000000-0000-0000-0000-000000000010',
     '00000000-0000-0000-0000-000000000002', '40000000-0000-0000-0000-000000000003')
ON CONFLICT (org_id, user_id, role_id) DO NOTHING;

-- RBAC ceilings:
--   org_admin → all 4 actions on folder + metadata_item (8 rows)
--   viewer    → read only on folder + metadata_item     (2 rows)
INSERT INTO access.role_permissions (id, role_id, action_id, resource_type)
SELECT
    gen_random_uuid(),
    r.id,
    pa.id,
    rt.val
FROM access.roles r
CROSS JOIN access.permission_actions pa
CROSS JOIN (VALUES ('folder'::access.resource_type), ('metadata_item'::access.resource_type)) AS rt(val)
WHERE (r.code = 'org_admin')
   OR (r.code = 'viewer' AND pa.code = 'read')
ON CONFLICT (role_id, action_id, resource_type) DO NOTHING;
