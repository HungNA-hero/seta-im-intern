CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE user_ref (
    user_id uuid PRIMARY KEY
);

COMMENT ON TABLE user_ref IS
    'Shadow reference to Node Access DB users. Stores only ids for asset audit fields.';

CREATE TABLE folders (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    parent_id uuid NULL REFERENCES folders(id) ON DELETE RESTRICT,
    name varchar(255) NOT NULL,
    description text,
    created_by uuid NOT NULL REFERENCES user_ref(user_id) ON DELETE RESTRICT,
    updated_by uuid NULL REFERENCES user_ref(user_id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz,
    CONSTRAINT chk_folders_name_not_blank CHECK (btrim(name) <> ''),
    CONSTRAINT chk_folders_not_parent_of_self CHECK (parent_id IS NULL OR parent_id <> id)
);

COMMENT ON TABLE folders IS
    'Folder tree owned by Go Asset Core. parent_id NULL means root folder.';

CREATE UNIQUE INDEX uq_folders_active_sibling_name
    ON folders (COALESCE(parent_id, '00000000-0000-0000-0000-000000000000'::uuid), name)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_folders_parent_id
    ON folders(parent_id);

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
    'Text-only image metadata records owned by Go Asset Core. Real image files are out of Phase 1 scope.';

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
