CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS ltree;

CREATE TABLE organization_ref (
    org_id uuid PRIMARY KEY
);

COMMENT ON TABLE organization_ref IS
    'Shadow reference to Node Access DB organizations.';

CREATE TABLE user_ref (
    user_id uuid PRIMARY KEY
);

COMMENT ON TABLE user_ref IS
    'Shadow reference to Node Access DB users. Stores only ids for asset audit fields.';

CREATE TABLE folders (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organization_ref(org_id) ON DELETE RESTRICT,
    path ltree NOT NULL,
    name varchar(255) NOT NULL,
    description text,
    created_by uuid NOT NULL REFERENCES user_ref(user_id) ON DELETE RESTRICT,
    updated_by uuid NULL REFERENCES user_ref(user_id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz,
    CONSTRAINT chk_folders_name_not_blank CHECK (btrim(name) <> '')
);

COMMENT ON TABLE folders IS
    'Folder tree owned by Go Asset Core, structured via ltree path.';

CREATE UNIQUE INDEX uq_folders_active_path
    ON folders (org_id, path)
    WHERE deleted_at IS NULL;

CREATE UNIQUE INDEX uq_folders_active_sibling_name
    ON folders (
        org_id,
        (
            CASE
                WHEN nlevel(path) > 1 THEN subpath(path, 0, nlevel(path) - 1)::text
                ELSE ''
            END
        ),
        name
    )
    WHERE deleted_at IS NULL;

CREATE INDEX idx_folders_path_gist
    ON folders USING GIST (path);

CREATE INDEX idx_folders_active
    ON folders(id)
    WHERE deleted_at IS NULL;

CREATE TABLE metadata_items (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    folder_id uuid NOT NULL REFERENCES folders(id) ON DELETE RESTRICT,
    title varchar(255) NOT NULL,
    description text,
    labels text[] NOT NULL DEFAULT '{}',
    category varchar(100),
    external_source varchar(100),
    external_id varchar(255),
    source_url text,
    thumbnail_url text,
    license varchar(255),
    author varchar(255),
    metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    notes text,
    created_by uuid NOT NULL REFERENCES user_ref(user_id) ON DELETE RESTRICT,
    updated_by uuid NULL REFERENCES user_ref(user_id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz,
    CONSTRAINT chk_metadata_items_title_not_blank CHECK (btrim(title) <> ''),
    CONSTRAINT chk_metadata_items_external_pair CHECK (
        (external_source IS NULL AND external_id IS NULL)
        OR (external_source IS NOT NULL AND external_id IS NOT NULL)
    )
);

COMMENT ON TABLE metadata_items IS
    'Text-only image metadata records owned by Go Asset Core. org context is resolved through folders.org_id.';

CREATE UNIQUE INDEX uq_metadata_items_external_identity
    ON metadata_items(external_source, external_id)
    WHERE external_source IS NOT NULL AND external_id IS NOT NULL;

CREATE INDEX idx_metadata_items_folder_id
    ON metadata_items(folder_id);

CREATE INDEX idx_metadata_items_active_folder_id
    ON metadata_items(folder_id)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_metadata_items_labels
    ON metadata_items USING gin(labels);

CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS trigger AS $$
BEGIN
    IF OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL THEN
        RETURN NEW;
    END IF;
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_folders_set_updated_at
BEFORE UPDATE ON folders
FOR EACH ROW
EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_metadata_items_set_updated_at
BEFORE UPDATE ON metadata_items
FOR EACH ROW
EXECUTE FUNCTION set_updated_at();

CREATE OR REPLACE FUNCTION chk_metadata_folder_active()
RETURNS trigger AS $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM folders
        WHERE id = NEW.folder_id AND deleted_at IS NOT NULL
    ) THEN
        RAISE EXCEPTION
            'Cannot assign metadata item to soft-deleted folder %', NEW.folder_id
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_metadata_items_folder_active
BEFORE INSERT OR UPDATE OF folder_id ON metadata_items
FOR EACH ROW
EXECUTE FUNCTION chk_metadata_folder_active();
