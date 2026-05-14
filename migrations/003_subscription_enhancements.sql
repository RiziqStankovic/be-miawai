-- Tabel transaksi pembayaran untuk Midtrans dan Google Play
CREATE TABLE IF NOT EXISTS payment_transactions (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  order_id TEXT NOT NULL UNIQUE,  -- Midtrans order_id atau Google Play purchase token
  platform TEXT NOT NULL,         -- 'midtrans', 'google_play'
  product_id TEXT NOT NULL,
  amount INTEGER NOT NULL DEFAULT 0,
  currency TEXT NOT NULL DEFAULT 'IDR',
  status TEXT NOT NULL DEFAULT 'pending',  -- pending, settlement, expire, cancel, refund
  raw_payload JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Penggunaan kuota per hari (Prompt Counting)
CREATE TABLE IF NOT EXISTS user_daily_usage (
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  usage_date DATE NOT NULL DEFAULT CURRENT_DATE,
  prompt_count INTEGER NOT NULL DEFAULT 0,
  token_input INTEGER NOT NULL DEFAULT 0,
  token_output INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (user_id, usage_date)
);

CREATE INDEX IF NOT EXISTS idx_payment_transactions_user_id ON payment_transactions(user_id);
CREATE INDEX IF NOT EXISTS idx_payment_transactions_order_id ON payment_transactions(order_id);
CREATE INDEX IF NOT EXISTS idx_user_daily_usage_date ON user_daily_usage(user_id, usage_date);

-- Support Cloud Sync untuk Conversation di Hybrid mode
ALTER TABLE conversations ADD COLUMN IF NOT EXISTS is_cloud_synced BOOLEAN NOT NULL DEFAULT FALSE;
