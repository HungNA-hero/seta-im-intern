-- Seed permission actions, roles, and RBAC role_permissions (S1-007)
-- Fixed UUIDs allow idempotent references from application code and future migrations.

INSERT INTO access.permission_actions (id, name, description) VALUES
    ('a0000000-0000-0000-0000-000000000001', 'read',               'Read a folder or metadata item'),
    ('a0000000-0000-0000-0000-000000000002', 'write',              'Create, update, or delete a folder or metadata item'),
    ('a0000000-0000-0000-0000-000000000003', 'manage_permissions', 'Grant or revoke permissions on a folder or metadata item');

INSERT INTO access.roles (id, name, display_name) VALUES
    ('b0000000-0000-0000-0000-000000000001', 'trainer_admin', 'Admin'),
    ('b0000000-0000-0000-0000-000000000002', 'editor',        'Editor'),
    ('b0000000-0000-0000-0000-000000000003', 'viewer',        'Viewer');

-- trainer_admin: all actions on all resource types (global override role)
-- editor: read + write on all resource types; no manage_permissions (OLP scoped)
-- viewer: read only on all resource types; OLP write grants are ignored at evaluation time
INSERT INTO access.role_permissions (role_id, action_id, resource_type)
SELECT
    r.id,
    pa.id,
    rt.val
FROM access.roles r
CROSS JOIN access.permission_actions pa
CROSS JOIN (VALUES ('folder'::access.resource_type), ('metadata_item'::access.resource_type)) AS rt(val)
WHERE (r.name = 'trainer_admin')
   OR (r.name = 'editor' AND pa.name IN ('read', 'write'))
   OR (r.name = 'viewer' AND pa.name = 'read');
