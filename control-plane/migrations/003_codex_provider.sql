ALTER TABLE accounts ADD COLUMN IF NOT EXISTS external_account_id text;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS account_email text;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS plan_type text;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS subscription_expires_at timestamptz;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS token_expires_at timestamptz;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS last_credential_refresh_at timestamptz;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS credential_persistence_status text NOT NULL DEFAULT 'persisted';

CREATE UNIQUE INDEX IF NOT EXISTS model_aliases_alias_folded_uq ON model_aliases (lower(alias));

CREATE TABLE IF NOT EXISTS discovered_models (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  provider_id uuid NOT NULL REFERENCES providers(id),
  account_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  upstream_model text NOT NULL,
  display_name text NOT NULL,
  capabilities jsonb NOT NULL DEFAULT '{}',
  visibility text NOT NULL DEFAULT 'unknown',
  supported_in_api boolean NOT NULL DEFAULT false,
  available boolean NOT NULL DEFAULT true,
  raw_metadata jsonb NOT NULL DEFAULT '{}',
  first_seen_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(account_id, upstream_model)
);

CREATE TABLE IF NOT EXISTS account_provider_state (
  account_id uuid PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
  quota jsonb NOT NULL DEFAULT '{}',
  reset_credits jsonb NOT NULL DEFAULT '{}',
  capability_status text NOT NULL DEFAULT 'unknown',
  fetched_at timestamptz,
  last_operation text,
  last_error_code text,
  last_error_message text,
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS provider_operations (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  account_id uuid NOT NULL REFERENCES accounts(id),
  operation_type text NOT NULL,
  idempotency_key text NOT NULL,
  status text NOT NULL CHECK (status IN ('pending','succeeded','failed','unknown')),
  lease_expires_at timestamptz,
  heartbeat_at timestamptz,
  preflight jsonb NOT NULL DEFAULT '{}',
  upstream_request_id text,
  result_summary jsonb NOT NULL DEFAULT '{}',
  warning_code text,
  resolution_source text,
  resolved_by text,
  resolution_note text,
  resolved_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz,
  completed_at timestamptz,
  UNIQUE(account_id, operation_type, idempotency_key)
);

CREATE UNIQUE INDEX IF NOT EXISTS provider_operations_one_active_reset
ON provider_operations(account_id)
WHERE operation_type='quota_reset' AND status IN ('pending','unknown');
