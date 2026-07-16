-- Seed permission actions (read, write, delete, manage_permissions)
INSERT INTO access.permission_actions (id, code, description) VALUES
    ('30000000-0000-0000-0000-000000000001', 'read',               'Allows viewing a resource'),
    ('30000000-0000-0000-0000-000000000002', 'write',              'Allows creating or updating a resource'),
    ('30000000-0000-0000-0000-000000000003', 'delete',             'Allows deleting a resource'),
    ('30000000-0000-0000-0000-000000000004', 'manage_permissions', 'Allows granting or revoking access on a resource')
ON CONFLICT (code) DO NOTHING;
