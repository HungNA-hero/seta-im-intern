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
    -- Fast-path guard: blocks the trivial A→A self-loop at the row level.
    -- Multi-hop cycles (A→B→A etc.) are caught by trg_folders_no_cycle below.
    CONSTRAINT chk_folders_not_parent_of_self CHECK (parent_id IS NULL OR parent_id <> id)
);

COMMENT ON TABLE folders IS
    'Folder tree owned by Go Asset Core. parent_id NULL means root folder.';

-- Cycle detection: blocks multi-hop cycles such as A→B→A that the single-row
-- CHECK constraint (chk_folders_not_parent_of_self) cannot catch.
-- Walks the full ancestor chain of NEW.parent_id; raises if NEW.id appears in it.
CREATE OR REPLACE FUNCTION folders_no_cycle()
RETURNS trigger AS $$
BEGIN
    -- Only run when parent_id is set and actually changed
    IF NEW.parent_id IS NULL THEN
        RETURN NEW;
    END IF;

    -- Walk the ancestor chain of NEW.parent_id upward
    IF EXISTS (
        WITH RECURSIVE ancestors AS (
            SELECT parent_id AS ancestor_id
            FROM folders
            WHERE id = NEW.parent_id

            UNION ALL

            SELECT f.parent_id
            FROM folders f
            INNER JOIN ancestors a ON f.id = a.ancestor_id
            WHERE a.ancestor_id IS NOT NULL
        )
        SELECT 1 FROM ancestors WHERE ancestor_id = NEW.id
    ) THEN
        RAISE EXCEPTION
            'Cycle detected: setting parent_id = % on folder % would create a circular reference',
            NEW.parent_id, NEW.id
            USING ERRCODE = 'check_violation';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

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
    -- Skip updated_at stamp when the row is being soft-deleted,
    -- so consumers polling (updated_at > X AND deleted_at IS NULL) 
    -- won't miss the deletion event in the same sync window.
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

-- Cycle detection trigger: fires on every INSERT and on UPDATE when parent_id changes.
-- Calls folders_no_cycle() which walks the full ancestor chain to block A→B→A cycles.
CREATE TRIGGER trg_folders_no_cycle
BEFORE INSERT OR UPDATE OF parent_id ON folders
FOR EACH ROW
EXECUTE FUNCTION folders_no_cycle();

CREATE OR REPLACE FUNCTION chk_parent_folder_active()
RETURNS trigger AS $$
BEGIN
    IF NEW.parent_id IS NOT NULL AND EXISTS (
        SELECT 1 FROM folders
        WHERE id = NEW.parent_id AND deleted_at IS NOT NULL
    ) THEN
        RAISE EXCEPTION
            'Cannot set parent to soft-deleted folder %', NEW.parent_id
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_folders_parent_active
BEFORE INSERT OR UPDATE OF parent_id ON folders
FOR EACH ROW
EXECUTE FUNCTION chk_parent_folder_active();

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
