-- KAN-82: one lifecycle unit represents one Recycle Bin root. The root resource
-- remains in folders or metadata_items; this table does not duplicate the tree.
CREATE TABLE asset_lifecycle_units (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organization_ref(org_id) ON DELETE RESTRICT,
    root_resource_type varchar(16) NOT NULL,
    root_resource_id uuid NOT NULL,
    root_folder_path ltree NOT NULL,
    original_parent_path ltree,
    original_folder_id uuid,
    state varchar(24) NOT NULL,
    requested_by uuid NOT NULL REFERENCES user_ref(user_id) ON DELETE RESTRICT,
    delete_completed_at timestamptz,
    retention_until timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_asset_lifecycle_units_root_type CHECK (
        root_resource_type IN ('FOLDER', 'METADATA')
    ),
    CONSTRAINT chk_asset_lifecycle_units_state CHECK (
        state IN (
            'DELETE_QUEUED', 'DELETING', 'DELETED',
            'RESTORE_QUEUED', 'RESTORING',
            'RESTORED', 'PURGE_QUEUED', 'PURGING', 'FAILED', 'PURGED'
        )
    ),
    CONSTRAINT chk_asset_lifecycle_units_root_context CHECK (
        (root_resource_type = 'FOLDER' AND original_folder_id IS NULL)
        OR (root_resource_type = 'METADATA' AND original_folder_id IS NOT NULL)
    )
);

-- A root can have only one live lifecycle unit. RESTORED and PURGED records
-- are terminal and no longer reserve the logical root identity.
CREATE UNIQUE INDEX uq_asset_lifecycle_units_live_root
    ON asset_lifecycle_units (org_id, root_resource_type, root_resource_id)
    WHERE state NOT IN ('RESTORED', 'PURGED');

CREATE INDEX idx_asset_lifecycle_units_trash
    ON asset_lifecycle_units (org_id, delete_completed_at DESC, id ASC)
    WHERE state = 'DELETED';

CREATE INDEX idx_asset_lifecycle_units_retention
    ON asset_lifecycle_units (retention_until)
    WHERE state = 'DELETED' AND retention_until IS NOT NULL;

ALTER TABLE folders
    ADD COLUMN lifecycle_unit_id uuid NULL
    REFERENCES asset_lifecycle_units(id) ON DELETE SET NULL;

ALTER TABLE metadata_items
    ADD COLUMN lifecycle_unit_id uuid NULL
    REFERENCES asset_lifecycle_units(id) ON DELETE SET NULL;

CREATE INDEX idx_folders_lifecycle_unit_id
    ON folders (lifecycle_unit_id)
    WHERE lifecycle_unit_id IS NOT NULL;

CREATE INDEX idx_metadata_items_lifecycle_unit_id
    ON metadata_items (lifecycle_unit_id)
    WHERE lifecycle_unit_id IS NOT NULL;

CREATE OR REPLACE FUNCTION set_asset_lifecycle_unit_updated_at()
RETURNS trigger AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_asset_lifecycle_units_set_updated_at
BEFORE UPDATE ON asset_lifecycle_units
FOR EACH ROW
EXECUTE FUNCTION set_asset_lifecycle_unit_updated_at();
