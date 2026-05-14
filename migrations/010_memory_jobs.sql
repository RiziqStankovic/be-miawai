CREATE TABLE IF NOT EXISTS memory_extraction_jobs (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  user_message TEXT NOT NULL,
  assistant_reply TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  attempts INTEGER NOT NULL DEFAULT 0,
  max_attempts INTEGER NOT NULL DEFAULT 3,
  last_error TEXT NOT NULL DEFAULT '',
  run_after TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  locked_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_memory_extraction_jobs_due
  ON memory_extraction_jobs(status, run_after, created_at)
  WHERE status IN ('pending', 'retry');

CREATE INDEX IF NOT EXISTS idx_memory_extraction_jobs_user_updated
  ON memory_extraction_jobs(user_id, updated_at DESC);
