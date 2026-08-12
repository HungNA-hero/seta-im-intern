-- KAN-82: delete is no longer a standalone permission. Existing grants keep
-- their effective ability to delete through write, while the public and
-- persisted permission vocabulary becomes read/write/manage_permissions.
DO $$
DECLARE
    legacy_delete_action_id uuid;
    write_action_id uuid;
BEGIN
    SELECT id INTO legacy_delete_action_id
    FROM access.permission_actions
    WHERE code = 'delete';

    SELECT id INTO write_action_id
    FROM access.permission_actions
    WHERE code = 'write';

    IF write_action_id IS NULL THEN
        RAISE EXCEPTION 'permission action write is required before replacing legacy delete grants';
    END IF;

    IF legacy_delete_action_id IS NOT NULL THEN
        -- Keep the existing write row when a role already has both actions.
        DELETE FROM access.role_permissions AS legacy
        USING access.role_permissions AS replacement
        WHERE legacy.action_id = legacy_delete_action_id
          AND replacement.action_id = write_action_id
          AND replacement.role_id = legacy.role_id
          AND replacement.resource_type = legacy.resource_type;

        UPDATE access.role_permissions
        SET action_id = write_action_id
        WHERE action_id = legacy_delete_action_id;

        -- The object-permission uniqueness rules are split by user/role grants.
        -- IS NOT DISTINCT FROM treats the unused grantee column's NULL as equal,
        -- so only an exact same-subject write grant de-duplicates a delete grant.
        DELETE FROM access.object_permissions AS legacy
        WHERE legacy.action_id = legacy_delete_action_id
          AND EXISTS (
              SELECT 1
              FROM access.object_permissions AS replacement
              WHERE replacement.action_id = write_action_id
                AND replacement.org_id = legacy.org_id
                AND replacement.resource_type = legacy.resource_type
                AND replacement.resource_id = legacy.resource_id
                AND replacement.grantee_user_id IS NOT DISTINCT FROM legacy.grantee_user_id
                AND replacement.grantee_role_id IS NOT DISTINCT FROM legacy.grantee_role_id
          );

        UPDATE access.object_permissions
        SET action_id = write_action_id
        WHERE action_id = legacy_delete_action_id;

        DELETE FROM access.permission_actions
        WHERE id = legacy_delete_action_id;
    END IF;
END;
$$;

-- PostgreSQL cannot remove one enum value in place. Rebuild this enum only
-- after all rows have been converted, so Access DB itself cannot mint another
-- standalone delete action through a bypass of the GraphQL API.
ALTER TYPE access.permission_action_code RENAME TO permission_action_code_legacy;

CREATE TYPE access.permission_action_code AS ENUM (
    'read',
    'write',
    'manage_permissions'
);

ALTER TABLE access.permission_actions
    ALTER COLUMN code TYPE access.permission_action_code
    USING code::text::access.permission_action_code;

DROP TYPE access.permission_action_code_legacy;
