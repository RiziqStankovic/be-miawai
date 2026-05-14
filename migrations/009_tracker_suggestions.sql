CREATE TABLE IF NOT EXISTS tracker_suggestions (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  module TEXT NOT NULL,
  title TEXT NOT NULL,
  amount TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'tracked',
  category TEXT NOT NULL DEFAULT 'general',
  detail TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT 'Miaw AI Chat',
  updated_from TEXT NOT NULL DEFAULT 'Miaw AI extraction',
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  content_hash TEXT NOT NULL DEFAULT '',
  review_status TEXT NOT NULL DEFAULT 'pending',
  created_tracker_entry_id TEXT REFERENCES tracker_entries(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_tracker_suggestions_user_status_updated
  ON tracker_suggestions(user_id, review_status, updated_at DESC);

CREATE UNIQUE INDEX IF NOT EXISTS idx_tracker_suggestions_user_content_hash_pending
  ON tracker_suggestions(user_id, content_hash)
  WHERE review_status = 'pending';
