ALTER TABLE tracker_entries
  ADD COLUMN IF NOT EXISTS content_hash TEXT NOT NULL DEFAULT '';

UPDATE tracker_entries
SET content_hash = md5(user_id || '|' || module || '|' || title || '|' || amount || '|' || category || '|' || detail)
WHERE content_hash = '';

CREATE UNIQUE INDEX IF NOT EXISTS idx_tracker_entries_user_content_hash
  ON tracker_entries(user_id, content_hash)
  WHERE deleted_at IS NULL;
