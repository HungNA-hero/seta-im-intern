-- Indexes informed by EXPLAIN (ANALYZE, BUFFERS) benchmarking of
-- SearchMetadataItems against a 1M-row metadata_items table
-- (see infra/db/asset/loadtest/). Baseline showed:
--   * ILIKE title/description/notes search -> sequential scan, no index
--     existed for text search.
--   * category equality filter -> sequential scan, no index existed.
--   * ORDER BY updated_at DESC, id ASC (used by every SearchMetadataItems
--     call, filtered or not) -> always sorted from scratch; degrades to an
--     on-disk external merge sort at large OFFSETs.
-- external_source equality was NOT indexed here: in the benchmark dataset
-- nearly all rows share one external_source, so the planner correctly
-- prefers a sequential scan over an index for that specific filter, and an
-- index doesn't measurably help. Re-evaluate with a more source-diverse
-- dataset if external_source becomes a common filter.

CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- Supports ILIKE '%term%' search on the three free-text columns
-- SearchMetadataItems ORs together; combined via BitmapOr for the OR clause.
CREATE INDEX idx_metadata_items_active_title_trgm ON metadata_items USING gin (title gin_trgm_ops) WHERE deleted_at IS NULL;
CREATE INDEX idx_metadata_items_active_description_trgm ON metadata_items USING gin (description gin_trgm_ops) WHERE deleted_at IS NULL;
CREATE INDEX idx_metadata_items_active_notes_trgm ON metadata_items USING gin (notes gin_trgm_ops) WHERE deleted_at IS NULL;

CREATE INDEX idx_metadata_items_active_category ON metadata_items(category) WHERE deleted_at IS NULL;

-- Lets SearchMetadataItems' ORDER BY updated_at DESC, id ASC be satisfied by
-- an index scan instead of sorting the joined result set from scratch.
CREATE INDEX idx_metadata_items_active_updated_at ON metadata_items(updated_at DESC, id) WHERE deleted_at IS NULL;
