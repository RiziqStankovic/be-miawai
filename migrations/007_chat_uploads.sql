CREATE TABLE IF NOT EXISTS chat_uploads (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  conversation_id TEXT REFERENCES conversations(id) ON DELETE SET NULL,
  message_id TEXT,
  original_name TEXT NOT NULL DEFAULT '',
  mime_type TEXT NOT NULL,
  local_path TEXT NOT NULL,
  public_url TEXT NOT NULL,
  size_bytes INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_chat_uploads_user_created
  ON chat_uploads(user_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_chat_uploads_conversation
  ON chat_uploads(conversation_id, created_at ASC);
