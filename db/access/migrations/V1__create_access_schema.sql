-- Access schema: organization-scoped RBAC + object-level permissions.
-- Owned by Node Access Policy service. No cross-database joins or foreign keys.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE SCHEMA IF NOT EXISTS access;

CREATE TYPE access.permission_action_code AS ENUM (
    'read',
    'write',
    'delete',
    'manage_permissions'
);

CREATE TYPE access.resource_type AS ENUM ('folder', 'metadata_item');

CREATE OR REPLACE FUNCTION access.set_updated_at()
RETURNS trigger AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TABLE access.users (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    email        varchar(255) NOT NULL UNIQUE,
    display_name varchar(255) NOT NULL,
    is_active    boolean     NOT NULL DEFAULT true,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    deleted_at   timestamptz
);

CREATE TRIGGER trg_users_set_updated_at
BEFORE UPDATE ON access.users
FOR EACH ROW
EXECUTE FUNCTION access.set_updated_at();

CREATE TABLE access.organizations (
    id          uuid         PRIMARY KEY DEFAULT gen_random_uuid(),
    code        varchar(100) NOT NULL UNIQUE,
    name        varchar(255) NOT NULL,
    olp_enabled boolean      NOT NULL DEFAULT false,
    created_at  timestamptz  NOT NULL DEFAULT now(),
    updated_at  timestamptz  NOT NULL DEFAULT now(),
    CONSTRAINT chk_organizations_code_not_blank CHECK (btrim(code) <> ''),
    CONSTRAINT chk_organizations_name_not_blank CHECK (btrim(name) <> '')
);

CREATE TRIGGER trg_organizations_set_updated_at
BEFORE UPDATE ON access.organizations
FOR EACH ROW
EXECUTE FUNCTION access.set_updated_at();

CREATE TABLE access.organization_members (
    id        uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id    uuid        NOT NULL REFERENCES access.organizations(id) ON DELETE CASCADE,
    user_id   uuid        NOT NULL REFERENCES access.users(id) ON DELETE CASCADE,
    joined_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, user_id)
);

CREATE TABLE access.roles (
    id          uuid         PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      uuid         NOT NULL REFERENCES access.organizations(id) ON DELETE CASCADE,
    code        varchar(100) NOT NULL,
    name        varchar(255) NOT NULL,
    description text,
    created_at  timestamptz  NOT NULL DEFAULT now(),
    updated_at  timestamptz  NOT NULL DEFAULT now(),
    CONSTRAINT chk_roles_code_not_blank CHECK (btrim(code) <> ''),
    CONSTRAINT chk_roles_name_not_blank CHECK (btrim(name) <> ''),
    UNIQUE (org_id, code),
    UNIQUE (org_id, id)
);

CREATE TRIGGER trg_roles_set_updated_at
BEFORE UPDATE ON access.roles
FOR EACH ROW
EXECUTE FUNCTION access.set_updated_at();

CREATE TABLE access.user_roles (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      uuid        NOT NULL REFERENCES access.organizations(id) ON DELETE CASCADE,
    user_id     uuid        NOT NULL REFERENCES access.users(id) ON DELETE CASCADE,
    role_id     uuid        NOT NULL REFERENCES access.roles(id) ON DELETE CASCADE,
    assigned_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, user_id, role_id),
    FOREIGN KEY (org_id, user_id)
        REFERENCES access.organization_members (org_id, user_id)
        ON DELETE CASCADE,
    FOREIGN KEY (org_id, role_id)
        REFERENCES access.roles (org_id, id)
        ON DELETE CASCADE
);

CREATE TABLE access.permission_actions (
    id          uuid                          PRIMARY KEY DEFAULT gen_random_uuid(),
    code        access.permission_action_code NOT NULL UNIQUE,
    description text
);

CREATE TABLE access.role_permissions (
    id            uuid                 PRIMARY KEY DEFAULT gen_random_uuid(),
    role_id       uuid                 NOT NULL REFERENCES access.roles(id) ON DELETE CASCADE,
    action_id     uuid                 NOT NULL REFERENCES access.permission_actions(id) ON DELETE CASCADE,
    resource_type access.resource_type NOT NULL,
    UNIQUE (role_id, action_id, resource_type)
);

-- OLP grants are evaluated only when organizations.olp_enabled = true.
-- resource_id is a logical ID from Asset DB; no cross-DB FK is enforced.
CREATE TABLE access.object_permissions (
    id              uuid                 PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          uuid                 NOT NULL REFERENCES access.organizations(id) ON DELETE CASCADE,
    resource_type   access.resource_type NOT NULL,
    resource_id     uuid                 NOT NULL,
    grantee_user_id uuid                 REFERENCES access.users(id) ON DELETE CASCADE,
    grantee_role_id uuid                 REFERENCES access.roles(id) ON DELETE CASCADE,
    action_id       uuid                 NOT NULL REFERENCES access.permission_actions(id) ON DELETE CASCADE,
    granted_by      uuid                 NOT NULL REFERENCES access.users(id) ON DELETE RESTRICT,
    granted_at      timestamptz          NOT NULL DEFAULT now(),
    CONSTRAINT chk_object_perm_grantee CHECK (
        (grantee_user_id IS NOT NULL AND grantee_role_id IS NULL) OR
        (grantee_user_id IS NULL AND grantee_role_id IS NOT NULL)
    ),
    FOREIGN KEY (org_id, grantee_role_id)
        REFERENCES access.roles (org_id, id)
        ON DELETE CASCADE
);

CREATE UNIQUE INDEX uniq_object_perm_user
    ON access.object_permissions (
        org_id,
        resource_type,
        resource_id,
        grantee_user_id,
        action_id
    )
    WHERE grantee_user_id IS NOT NULL;

CREATE UNIQUE INDEX uniq_object_perm_role
    ON access.object_permissions (
        org_id,
        resource_type,
        resource_id,
        grantee_role_id,
        action_id
    )
    WHERE grantee_role_id IS NOT NULL;

CREATE INDEX idx_organization_members_user
    ON access.organization_members (user_id);

CREATE INDEX idx_roles_org_id
    ON access.roles (org_id);

CREATE INDEX idx_user_roles_user_org
    ON access.user_roles (user_id, org_id);

CREATE INDEX idx_role_permissions_role
    ON access.role_permissions (role_id);

CREATE INDEX idx_object_permissions_resource
    ON access.object_permissions (org_id, resource_type, resource_id);
