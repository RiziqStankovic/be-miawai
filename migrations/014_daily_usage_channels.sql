ALTER TABLE user_daily_usage ADD COLUMN IF NOT EXISTS image_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE user_daily_usage ADD COLUMN IF NOT EXISTS research_count INTEGER NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS user_usage_events (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_input INTEGER NOT NULL DEFAULT 0,
  token_output INTEGER NOT NULL DEFAULT 0,
  image_count INTEGER NOT NULL DEFAULT 0,
  research_count INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_user_usage_events_user_created
  ON user_usage_events(user_id, created_at DESC);
