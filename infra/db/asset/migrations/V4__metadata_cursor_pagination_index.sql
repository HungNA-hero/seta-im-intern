-- KAN-58: supports stable keyset traversal for active metadata in one folder.
-- Build concurrently so a populated production table remains writable while
-- this million-row-supporting index is created.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_metadata_items_active_folder_updated_id
    ON metadata_items(folder_id, updated_at DESC, id ASC)
    WHERE deleted_at IS NULL;
