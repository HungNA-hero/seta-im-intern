-- Seed permission actions (read, write, delete, manage_permissions)
INSERT INTO access.permission_actions (id, code, description) VALUES
    ('a0000000-0000-0000-0000-000000000001', 'read',               'Allows viewing a resource'),
    ('a0000000-0000-0000-0000-000000000002', 'write',              'Allows creating or updating a resource'),
    ('a0000000-0000-0000-0000-000000000003', 'delete',             'Allows deleting a resource'),
    ('a0000000-0000-0000-0000-000000000004', 'manage_permissions', 'Allows granting or revoking access on a resource')
ON CONFLICT (code) DO NOTHING;

-- Seed demo organization
INSERT INTO access.organizations (id, code, name, olp_enabled) VALUES
    ('c0000000-0000-0000-0000-000000000001', 'seta', 'Seta', false)
ON CONFLICT (id) DO NOTHING;

-- Seed roles for Seta org (org_admin = global override; viewer = read-only ceiling)
INSERT INTO access.roles (id, org_id, code, name, description) VALUES
    ('b0000000-0000-0000-0000-000000000001', 'c0000000-0000-0000-0000-000000000001',
     'org_admin', 'Org Admin', 'Full access to all resources and permissions within the org'),
    ('b0000000-0000-0000-0000-000000000002', 'c0000000-0000-0000-0000-000000000001',
     'viewer',    'Viewer',    'Read-only access to all resources within the org')
ON CONFLICT (id) DO NOTHING;

-- Seed demo users
INSERT INTO access.users (id, email, display_name, is_active) VALUES
    ('00000000-0000-0000-0000-000000000001', 'admin@seta.com',  'Seta Admin',    true),
    ('00000000-0000-0000-0000-000000000002', 'dungpd@seta.com', 'Dung Pham Duc', true)
ON CONFLICT (id) DO NOTHING;

-- Add both users to Seta org
INSERT INTO access.organization_members (id, org_id, user_id) VALUES
    ('d0000000-0000-0000-0000-000000000001', 'c0000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000001'),
    ('d0000000-0000-0000-0000-000000000002', 'c0000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000002')
ON CONFLICT (org_id, user_id) DO NOTHING;

-- Assign roles: admin → org_admin, dung → viewer
INSERT INTO access.user_roles (id, org_id, user_id, role_id) VALUES
    ('e0000000-0000-0000-0000-000000000001', 'c0000000-0000-0000-0000-000000000001',
     '00000000-0000-0000-0000-000000000001', 'b0000000-0000-0000-0000-000000000001'),
    ('e0000000-0000-0000-0000-000000000002', 'c0000000-0000-0000-0000-000000000001',
     '00000000-0000-0000-0000-000000000002', 'b0000000-0000-0000-0000-000000000002')
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
