ALTER TABLE media_upload_sessions
    ADD COLUMN raw_object_purged_at timestamptz;

COMMENT ON COLUMN media_upload_sessions.raw_object_purged_at IS
    'Set when the abandoned or invalid raw object behind this session has been removed from storage. Without it the quarantine sweep would reconsider every historical session on every pass, and a bounded batch would never reach newer candidates.';

-- The sweep measures quarantine from the moment a session became terminal, not
-- from updated_at: that column is trigger-maintained and moves for reasons that
-- have nothing to do with when the object was abandoned.
CREATE INDEX idx_media_upload_sessions_purge_due
    ON media_upload_sessions (COALESCE(cancelled_at, expired_at, committed_at))
    WHERE raw_object_purged_at IS NULL;
