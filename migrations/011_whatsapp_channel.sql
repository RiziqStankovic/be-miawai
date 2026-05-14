ALTER TABLE conversations ADD COLUMN IF NOT EXISTS channel TEXT NOT NULL DEFAULT 'web';
ALTER TABLE conversations ADD COLUMN IF NOT EXISTS channel_thread_id TEXT NOT NULL DEFAULT '';
ALTER TABLE conversations ADD COLUMN IF NOT EXISTS channel_display_name TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX IF NOT EXISTS idx_conversations_user_channel_thread
  ON conversations(user_id, channel, channel_thread_id)
  WHERE channel_thread_id <> '';

CREATE TABLE IF NOT EXISTS whatsapp_accounts (
  id TEXT PRIMARY KEY,
  owner_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  mode TEXT NOT NULL DEFAULT 'user_linked',
  display_name TEXT NOT NULL DEFAULT '',
  phone_jid TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'pending_qr',
  session_ref TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  CHECK (mode IN ('user_linked', 'central_bot')),
  CHECK (status IN ('pending_qr', 'connected', 'disconnected', 'revoked'))
);

CREATE INDEX IF NOT EXISTS idx_whatsapp_accounts_owner_status
  ON whatsapp_accounts(owner_user_id, status, updated_at DESC);

CREATE TABLE IF NOT EXISTS whatsapp_contacts (
  id TEXT PRIMARY KEY,
  whatsapp_account_id TEXT NOT NULL REFERENCES whatsapp_accounts(id) ON DELETE CASCADE,
  owner_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  contact_jid TEXT NOT NULL,
  display_name TEXT NOT NULL DEFAULT '',
  linked_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
  default_conversation_id TEXT REFERENCES conversations(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (whatsapp_account_id, contact_jid)
);

CREATE INDEX IF NOT EXISTS idx_whatsapp_contacts_owner
  ON whatsapp_contacts(owner_user_id, updated_at DESC);

CREATE INDEX IF NOT EXISTS idx_whatsapp_contacts_linked_user
  ON whatsapp_contacts(linked_user_id, updated_at DESC)
  WHERE linked_user_id IS NOT NULL;
