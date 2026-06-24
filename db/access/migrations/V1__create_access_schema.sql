-- Access schema: RBAC + object-level permission model (S1-007)
-- UC-PERM-001, UC-PERM-002

CREATE SCHEMA IF NOT EXISTS access;

CREATE TYPE access.resource_type AS ENUM ('folder', 'metadata_item');

CREATE OR REPLACE FUNCTION access.set_updated_at()
RETURNS trigger AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TABLE access.users (
    id           UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    username     VARCHAR(255) NOT NULL UNIQUE,
    email        VARCHAR(255) NOT NULL UNIQUE,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE TRIGGER trg_users_set_updated_at
BEFORE UPDATE ON access.users
FOR EACH ROW
EXECUTE FUNCTION access.set_updated_at();

CREATE TABLE access.roles (
    id           UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    name         VARCHAR(100) NOT NULL UNIQUE,
    display_name VARCHAR(255),
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE TABLE access.user_roles (
    user_id    UUID        NOT NULL REFERENCES access.users(id) ON DELETE CASCADE,
    role_id    UUID        NOT NULL REFERENCES access.roles(id) ON DELETE CASCADE,
    granted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    granted_by UUID        REFERENCES access.users(id),
    PRIMARY KEY (user_id, role_id)
);

CREATE TABLE access.permission_actions (
    id          UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    name        VARCHAR(100) NOT NULL UNIQUE,
    description TEXT
);

-- RBAC layer: capability ceiling — what a role may do on a resource type
CREATE TABLE access.role_permissions (
    role_id       UUID                 NOT NULL REFERENCES access.roles(id) ON DELETE CASCADE,
    action_id     UUID                 NOT NULL REFERENCES access.permission_actions(id) ON DELETE CASCADE,
    resource_type access.resource_type NOT NULL,
    PRIMARY KEY (role_id, action_id, resource_type)
);

-- OLP layer: folder-level object grants
-- Exactly one of grantee_user_id / grantee_role_id must be set.
-- Partial unique indexes handle NULLs correctly (standard UNIQUE cannot).
CREATE TABLE access.folder_permissions (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    folder_id       UUID        NOT NULL,
    grantee_user_id UUID        REFERENCES access.users(id) ON DELETE CASCADE,
    grantee_role_id UUID        REFERENCES access.roles(id) ON DELETE CASCADE,
    action_id       UUID        NOT NULL REFERENCES access.permission_actions(id) ON DELETE CASCADE,
    granted_by      UUID        REFERENCES access.users(id),
    granted_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_folder_perm_grantee CHECK (
        (grantee_user_id IS NOT NULL AND grantee_role_id IS NULL) OR
        (grantee_user_id IS NULL     AND grantee_role_id IS NOT NULL)
    )
);

CREATE UNIQUE INDEX uniq_folder_perm_user
    ON access.folder_permissions (folder_id, grantee_user_id, action_id)
    WHERE grantee_user_id IS NOT NULL;

CREATE UNIQUE INDEX uniq_folder_perm_role
    ON access.folder_permissions (folder_id, grantee_role_id, action_id)
    WHERE grantee_role_id IS NOT NULL;

-- OLP layer: metadata-item-level object grants
CREATE TABLE access.metadata_permissions (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    metadata_item_id UUID        NOT NULL,
    grantee_user_id  UUID        REFERENCES access.users(id) ON DELETE CASCADE,
    grantee_role_id  UUID        REFERENCES access.roles(id) ON DELETE CASCADE,
    action_id        UUID        NOT NULL REFERENCES access.permission_actions(id) ON DELETE CASCADE,
    granted_by       UUID        REFERENCES access.users(id),
    granted_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_metadata_perm_grantee CHECK (
        (grantee_user_id IS NOT NULL AND grantee_role_id IS NULL) OR
        (grantee_user_id IS NULL     AND grantee_role_id IS NOT NULL)
    )
);

CREATE UNIQUE INDEX uniq_metadata_perm_user
    ON access.metadata_permissions (metadata_item_id, grantee_user_id, action_id)
    WHERE grantee_user_id IS NOT NULL;

CREATE UNIQUE INDEX uniq_metadata_perm_role
    ON access.metadata_permissions (metadata_item_id, grantee_role_id, action_id)
    WHERE grantee_role_id IS NOT NULL;

-- Supporting indexes
CREATE INDEX idx_user_roles_role        ON access.user_roles (role_id);
CREATE INDEX idx_folder_perm_folder     ON access.folder_permissions (folder_id);
CREATE INDEX idx_metadata_perm_item     ON access.metadata_permissions (metadata_item_id);
