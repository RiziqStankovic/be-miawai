ALTER TABLE whatsapp_accounts ADD COLUMN IF NOT EXISTS access_policy TEXT NOT NULL DEFAULT 'allow_all';
ALTER TABLE whatsapp_accounts ADD COLUMN IF NOT EXISTS qr_code TEXT NOT NULL DEFAULT '';

ALTER TABLE whatsapp_contacts ADD COLUMN IF NOT EXISTS allow_status TEXT NOT NULL DEFAULT 'pending';
ALTER TABLE whatsapp_contacts ADD COLUMN IF NOT EXISTS verification_attempts INTEGER NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS whatsapp_link_codes (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  phone_number TEXT NOT NULL,
  code TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  attempts INTEGER NOT NULL DEFAULT 0,
  expires_at TIMESTAMPTZ NOT NULL,
  matched_contact_jid TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  CHECK (status IN ('pending', 'verified', 'expired', 'locked'))
);

CREATE INDEX IF NOT EXISTS idx_whatsapp_link_codes_user_status
  ON whatsapp_link_codes(user_id, status, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_whatsapp_link_codes_phone_status
  ON whatsapp_link_codes(phone_number, status, expires_at DESC);
