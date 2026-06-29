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
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    email        VARCHAR     NOT NULL UNIQUE,
    display_name VARCHAR     NOT NULL,
    is_active    BOOLEAN     NOT NULL DEFAULT true,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at   TIMESTAMPTZ
);

CREATE TRIGGER trg_users_set_updated_at
BEFORE UPDATE ON access.users
FOR EACH ROW EXECUTE FUNCTION access.set_updated_at();

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
FOR EACH ROW EXECUTE FUNCTION access.set_updated_at();

CREATE TABLE access.organization_members (
    id        UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id    UUID        NOT NULL REFERENCES access.organizations(id) ON DELETE CASCADE,
    user_id   UUID        NOT NULL REFERENCES access.users(id) ON DELETE CASCADE,
    joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uniq_org_member UNIQUE (org_id, user_id)
);

CREATE INDEX idx_org_members_user ON access.organization_members (user_id);

CREATE TABLE access.roles (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      UUID        NOT NULL REFERENCES access.organizations(id) ON DELETE CASCADE,
    code        VARCHAR     NOT NULL,
    name        VARCHAR     NOT NULL,
    description TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uniq_role_code_per_org UNIQUE (org_id, code),
    CONSTRAINT uq_roles_id_org UNIQUE (id, org_id)
);

CREATE TRIGGER trg_roles_set_updated_at
BEFORE UPDATE ON access.roles
FOR EACH ROW EXECUTE FUNCTION access.set_updated_at();

CREATE INDEX idx_roles_org ON access.roles (org_id);

CREATE TABLE access.user_roles (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      UUID        NOT NULL REFERENCES access.organizations(id) ON DELETE CASCADE,
    user_id     UUID        NOT NULL REFERENCES access.users(id) ON DELETE CASCADE,
    role_id     UUID        NOT NULL,
    assigned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT fk_user_roles_role_org FOREIGN KEY (role_id, org_id)
        REFERENCES access.roles(id, org_id) ON DELETE CASCADE,
    CONSTRAINT uniq_user_role_per_org UNIQUE (org_id, user_id, role_id)
);

CREATE INDEX idx_user_roles_org_user ON access.user_roles (org_id, user_id);

CREATE TABLE access.permission_actions (
    id          UUID                          PRIMARY KEY DEFAULT gen_random_uuid(),
    code        access.permission_action_code NOT NULL UNIQUE,
    description TEXT
);

CREATE TABLE access.role_permissions (
    id            UUID                 PRIMARY KEY DEFAULT gen_random_uuid(),
    role_id       UUID                 NOT NULL REFERENCES access.roles(id) ON DELETE CASCADE,
    action_id     UUID                 NOT NULL REFERENCES access.permission_actions(id) ON DELETE CASCADE,
    resource_type access.resource_type NOT NULL,
    CONSTRAINT uniq_role_permission UNIQUE (role_id, action_id, resource_type)
);

CREATE INDEX idx_role_perms_role ON access.role_permissions (role_id);

CREATE TABLE access.object_permissions (
    id              UUID                 PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          UUID                 NOT NULL REFERENCES access.organizations(id) ON DELETE CASCADE,
    resource_type   access.resource_type NOT NULL,
    resource_id     UUID                 NOT NULL,
    grantee_user_id UUID                 REFERENCES access.users(id) ON DELETE CASCADE,
    grantee_role_id UUID,
    action_id       UUID                 NOT NULL REFERENCES access.permission_actions(id) ON DELETE CASCADE,
    granted_by      UUID                 NOT NULL REFERENCES access.users(id),
    granted_at      TIMESTAMPTZ          NOT NULL DEFAULT now(),
    CONSTRAINT fk_op_grantee_role_org FOREIGN KEY (grantee_role_id, org_id)
        REFERENCES access.roles(id, org_id) ON DELETE CASCADE,
    CONSTRAINT chk_object_perm_grantee CHECK (
        (grantee_user_id IS NOT NULL AND grantee_role_id IS NULL) OR
        (grantee_user_id IS NULL     AND grantee_role_id IS NOT NULL)
    )
);

CREATE UNIQUE INDEX uniq_object_perm_user
    ON access.object_permissions (org_id, resource_type, resource_id, grantee_user_id, action_id)
    WHERE grantee_user_id IS NOT NULL;

CREATE UNIQUE INDEX uniq_object_perm_role
    ON access.object_permissions (org_id, resource_type, resource_id, grantee_role_id, action_id)
    WHERE grantee_role_id IS NOT NULL;

CREATE INDEX idx_object_perm_object
    ON access.object_permissions (org_id, resource_type, resource_id);
