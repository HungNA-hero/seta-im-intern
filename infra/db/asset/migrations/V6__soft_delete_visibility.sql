-- KAN-70: lifecycle visibility is now tombstone-based. A deleted ancestor
-- hides its entire subtree, while every row remains available for a
-- parent-first restore.

CREATE INDEX idx_folders_deleted_path_gist
    ON folders USING GIST (path)
    WHERE deleted_at IS NOT NULL;

-- A deleted metadata item releases its external identity. If that identity is
-- reused by a new active item, restoring the old item returns METADATA_CONFLICT
-- rather than silently creating two active identities.
DROP INDEX IF EXISTS uq_metadata_items_external_identity;

CREATE UNIQUE INDEX uq_metadata_items_external_identity
    ON metadata_items(external_source, external_id)
    WHERE external_source IS NOT NULL
      AND external_id IS NOT NULL
      AND deleted_at IS NULL;

-- Keep the database invariant true even if a future write path bypasses the
-- repository. The immediate parent can be active while one of its ancestors
-- is tombstoned, so the old direct-parent check was insufficient.
CREATE OR REPLACE FUNCTION chk_metadata_folder_active()
RETURNS trigger AS $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM folders AS target
        JOIN folders AS deleted_ancestor
          ON deleted_ancestor.org_id = target.org_id
         AND deleted_ancestor.path @> target.path
         AND deleted_ancestor.deleted_at IS NOT NULL
        WHERE target.id = NEW.folder_id
    ) THEN
        RAISE EXCEPTION
            'Cannot assign metadata item to a folder with a soft-deleted ancestor %', NEW.folder_id
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
