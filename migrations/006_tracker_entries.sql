CREATE TABLE IF NOT EXISTS tracker_entries (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  module TEXT NOT NULL,
  title TEXT NOT NULL,
  amount TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'tracked',
  category TEXT NOT NULL DEFAULT 'general',
  detail TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT 'manual',
  updated_from TEXT NOT NULL DEFAULT 'manual',
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  deleted_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_tracker_entries_user_module_updated
  ON tracker_entries(user_id, module, updated_at DESC)
  WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_tracker_entries_user_updated
  ON tracker_entries(user_id, updated_at DESC)
  WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_tracker_entries_search
  ON tracker_entries
  USING GIN (to_tsvector('simple', coalesce(title, '') || ' ' || coalesce(detail, '') || ' ' || coalesce(category, '')))
  WHERE deleted_at IS NULL;
