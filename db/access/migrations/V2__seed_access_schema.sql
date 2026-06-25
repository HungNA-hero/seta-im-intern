-- Seed local Access DB data for organization-scoped RBAC and OLP checks.
-- Fixed UUIDs align with Asset DB shadow reference seed data.

INSERT INTO access.users (id, email, display_name)
VALUES (
    '00000000-0000-0000-0000-000000000001',
    'demo.user@seta.local',
    'Demo User'
);

INSERT INTO access.organizations (id, code, name, olp_enabled)
VALUES (
    '00000000-0000-0000-0000-000000000010',
    'seta',
    'Seta',
    true
);

INSERT INTO access.organization_members (id, org_id, user_id)
VALUES (
    '30000000-0000-0000-0000-000000000001',
    '00000000-0000-0000-0000-000000000010',
    '00000000-0000-0000-0000-000000000001'
);

INSERT INTO access.permission_actions (id, code, description) VALUES
    ('a0000000-0000-0000-0000-000000000001', 'read', 'Read a folder or metadata item'),
    ('a0000000-0000-0000-0000-000000000002', 'write', 'Create or update a folder or metadata item'),
    ('a0000000-0000-0000-0000-000000000003', 'delete', 'Delete a folder or metadata item'),
    ('a0000000-0000-0000-0000-000000000004', 'manage_permissions', 'Grant or revoke permissions on a folder or metadata item');

INSERT INTO access.roles (id, org_id, code, name, description) VALUES
    (
        'b0000000-0000-0000-0000-000000000001',
        '00000000-0000-0000-0000-000000000010',
        'org_admin',
        'Org Admin',
        'Can manage the organization, RBAC, OLP grants, folders, and metadata'
    ),
    (
        'b0000000-0000-0000-0000-000000000002',
        '00000000-0000-0000-0000-000000000010',
        'editor',
        'Editor',
        'Can read and write folders and metadata in the organization'
    ),
    (
        'b0000000-0000-0000-0000-000000000003',
        '00000000-0000-0000-0000-000000000010',
        'viewer',
        'Viewer',
        'Can read folders and metadata in the organization'
    );

INSERT INTO access.user_roles (id, org_id, user_id, role_id)
VALUES (
    '40000000-0000-0000-0000-000000000001',
    '00000000-0000-0000-0000-000000000010',
    '00000000-0000-0000-0000-000000000001',
    'b0000000-0000-0000-0000-000000000001'
);

-- org_admin: all actions on all resource types.
-- editor: read + write; delete and manage_permissions stay admin-only.
-- viewer: read only. Viewer write/delete overrides must come from object_permissions
-- and are evaluated only when organizations.olp_enabled is true.
INSERT INTO access.role_permissions (role_id, action_id, resource_type)
SELECT
    r.id,
    pa.id,
    rt.val
FROM access.roles r
CROSS JOIN access.permission_actions pa
CROSS JOIN (
    VALUES
        ('folder'::access.resource_type),
        ('metadata_item'::access.resource_type)
) AS rt(val)
WHERE (r.code = 'org_admin')
   OR (r.code = 'editor' AND pa.code IN ('read', 'write'))
   OR (r.code = 'viewer' AND pa.code = 'read');
