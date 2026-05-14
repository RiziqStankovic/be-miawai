CREATE TABLE IF NOT EXISTS user_memories (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  domain TEXT NOT NULL DEFAULT 'chat',
  kind TEXT NOT NULL DEFAULT 'fact',
  title TEXT NOT NULL,
  content TEXT NOT NULL,
  content_hash TEXT NOT NULL DEFAULT '',
  source_conversation_id TEXT REFERENCES conversations(id) ON DELETE SET NULL,
  confidence REAL NOT NULL DEFAULT 0.7,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  deleted_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_user_memories_user_updated_at
  ON user_memories(user_id, updated_at DESC)
  WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_user_memories_user_domain
  ON user_memories(user_id, domain, updated_at DESC)
  WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_user_memories_search
  ON user_memories
  USING GIN (to_tsvector('simple', coalesce(title, '') || ' ' || coalesce(content, '')))
  WHERE deleted_at IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_user_memories_user_content_hash
  ON user_memories(user_id, content_hash)
  WHERE deleted_at IS NULL;
