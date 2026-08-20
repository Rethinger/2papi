-- P0: spend ledger, key budgets, TPM/concurrency limits, model pricing and fallback chains.

ALTER TABLE virtual_keys ADD COLUMN IF NOT EXISTS budget_usd numeric(12,6) NOT NULL DEFAULT 0;
ALTER TABLE virtual_keys ADD COLUMN IF NOT EXISTS tpm integer NOT NULL DEFAULT 0;
ALTER TABLE virtual_keys ADD COLUMN IF NOT EXISTS max_concurrency integer NOT NULL DEFAULT 0;

ALTER TABLE model_aliases ADD COLUMN IF NOT EXISTS fallbacks text[] NOT NULL DEFAULT '{}';

CREATE TABLE IF NOT EXISTS model_pricing (
  model_alias_id uuid PRIMARY KEY REFERENCES model_aliases(id) ON DELETE CASCADE,
  input_per_mtok numeric(12,6) NOT NULL DEFAULT 0,
  output_per_mtok numeric(12,6) NOT NULL DEFAULT 0,
  updated_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE request_events ADD COLUMN IF NOT EXISTS cost_usd numeric(12,6) NOT NULL DEFAULT 0;
ALTER TABLE request_events ADD COLUMN IF NOT EXISTS virtual_key_id uuid REFERENCES virtual_keys(id) ON DELETE SET NULL;
ALTER TABLE request_event_attempts ADD COLUMN IF NOT EXISTS alias text NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS key_spend_daily (
  virtual_key_id uuid NOT NULL REFERENCES virtual_keys(id) ON DELETE CASCADE,
  day date NOT NULL,
  cost_usd numeric(12,6) NOT NULL DEFAULT 0,
  tokens_in bigint NOT NULL DEFAULT 0,
  tokens_out bigint NOT NULL DEFAULT 0,
  requests bigint NOT NULL DEFAULT 0,
  PRIMARY KEY (virtual_key_id, day)
);

CREATE INDEX IF NOT EXISTS key_spend_daily_day_idx ON key_spend_daily (day);
CREATE INDEX IF NOT EXISTS request_events_virtual_key_occurred_idx ON request_events (virtual_key_id, occurred_at DESC);
