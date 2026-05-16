CREATE TABLE IF NOT EXISTS desktop_auth_codes (
  code TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at TIMESTAMPTZ NOT NULL,
  used_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_desktop_auth_codes_user_created
  ON desktop_auth_codes(user_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_desktop_auth_codes_expires
  ON desktop_auth_codes(expires_at);
