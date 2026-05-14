CREATE TABLE IF NOT EXISTS whatsapp_events (
  id TEXT PRIMARY KEY,
  whatsapp_account_id TEXT NOT NULL REFERENCES whatsapp_accounts(id) ON DELETE CASCADE,
  contact_jid TEXT NOT NULL,
  sender_jid TEXT NOT NULL DEFAULT '',
  direction TEXT NOT NULL,
  message_id TEXT NOT NULL DEFAULT '',
  conversation_id TEXT NOT NULL DEFAULT '',
  text TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  CHECK (direction IN ('incoming', 'outgoing', 'system'))
);

CREATE INDEX IF NOT EXISTS idx_whatsapp_events_account_created
  ON whatsapp_events(whatsapp_account_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_whatsapp_events_contact_created
  ON whatsapp_events(whatsapp_account_id, contact_jid, created_at DESC);
