-- P4 (Cloud): prepaid credit ledger per team with idempotent grants.
-- Design: plan/2papi-3-editions-strategy.md (migration 012).
-- balance_usd is updated in the SAME transaction as each ledger insert.

CREATE TABLE IF NOT EXISTS credit_transactions (
  id bigserial PRIMARY KEY,
  team_id uuid NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  delta_usd numeric(12,6) NOT NULL,
  kind text NOT NULL CHECK (kind IN ('topup','bonus','refund','adjustment')),
  source text NOT NULL CHECK (source IN ('paddle','crypto_usdt','manual','signup_bonus')),
  external_id text NOT NULL DEFAULT '',
  note text NOT NULL DEFAULT '',
  created_by text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now()
);

-- Idempotency: Paddle webhook retries / duplicate txhash submissions
-- must never double-credit. Empty external_id (manual adjustments)
-- bypasses uniqueness via partial index below.
CREATE UNIQUE INDEX IF NOT EXISTS credit_tx_source_external_uq
  ON credit_transactions (source, external_id)
  WHERE external_id <> '';

CREATE INDEX IF NOT EXISTS credit_tx_team_created_idx
  ON credit_transactions (team_id, created_at DESC);

ALTER TABLE teams ADD COLUMN IF NOT EXISTS balance_usd numeric(12,6) NOT NULL DEFAULT 0;
