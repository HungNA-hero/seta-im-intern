-- Rewrites idx_metadata_items_active_category (from V2) as a composite index
-- per the funnel principle: when a query both filters (category =) and sorts
-- (updated_at DESC, id) together, a single composite index with the filter
-- column(s) first and the sort column(s) last lets Postgres satisfy both in
-- one ordered index scan, instead of choosing between "filter then sort" or
-- "scan the sort order and check the filter per row" (what the planner did
-- with the old single-column index — see infra/db/asset/loadtest/results.md).
--
-- Superseding a single-column index with this composite is safe: any query
-- that only filters on category (no updated_at ordering) still uses this
-- index's category prefix, same as the plain index did.

DROP INDEX IF EXISTS idx_metadata_items_active_category;

CREATE INDEX idx_metadata_items_active_category_updated_at
    ON metadata_items(category, updated_at DESC, id)
    WHERE deleted_at IS NULL;
