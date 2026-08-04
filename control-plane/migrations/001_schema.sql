CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS providers (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  slug text UNIQUE NOT NULL,
  name text NOT NULL,
  adapter text NOT NULL DEFAULT 'openai-compatible',
  base_url text NOT NULL,
  enabled boolean NOT NULL DEFAULT true,
  metadata jsonb NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS secret_records (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  purpose text NOT NULL,
  key_version integer NOT NULL,
  data_key_nonce bytea NOT NULL,
  data_key_ciphertext bytea NOT NULL,
  data_key_tag bytea NOT NULL,
  secret_nonce bytea NOT NULL,
  secret_ciphertext bytea NOT NULL,
  secret_tag bytea NOT NULL,
  rotated_at timestamptz NOT NULL DEFAULT now(),
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS accounts (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  provider_id uuid NOT NULL REFERENCES providers(id),
  secret_record_id uuid REFERENCES secret_records(id),
  name text UNIQUE NOT NULL,
  display_name text NOT NULL,
  base_url text NOT NULL,
  enabled boolean NOT NULL DEFAULT true,
  priority integer NOT NULL DEFAULT 1,
  weight integer NOT NULL DEFAULT 1 CHECK (weight > 0),
  max_concurrency integer NOT NULL DEFAULT 100 CHECK (max_concurrency > 0),
  cost numeric(12,6) NOT NULL DEFAULT 0,
  metadata jsonb NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS model_aliases (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  alias text UNIQUE NOT NULL,
  upstream_model text NOT NULL,
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS model_account_mappings (
  model_alias_id uuid NOT NULL REFERENCES model_aliases(id) ON DELETE CASCADE,
  account_id uuid NOT NULL REFERENCES accounts(id),
  enabled boolean NOT NULL DEFAULT true,
  tier integer NOT NULL DEFAULT 1,
  position integer NOT NULL DEFAULT 0,
  PRIMARY KEY (model_alias_id, account_id)
);

CREATE TABLE IF NOT EXISTS routing_settings (
  id boolean PRIMARY KEY DEFAULT true CHECK (id),
  strategy text NOT NULL DEFAULT 'balanced',
  sticky_ttl text NOT NULL DEFAULT '1h',
  max_attempts integer NOT NULL DEFAULT 2 CHECK (max_attempts > 0),
  resilience jsonb NOT NULL DEFAULT '{"cooldown":"30s","circuit_failures":3,"circuit_reset":"1m"}'
);

CREATE TABLE IF NOT EXISTS virtual_keys (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name text UNIQUE NOT NULL,
  key_hash text NOT NULL,
  key_prefix text NOT NULL,
  enabled boolean NOT NULL DEFAULT true,
  models text[] NOT NULL DEFAULT '{}',
  rpm integer NOT NULL DEFAULT 60,
  created_at timestamptz NOT NULL DEFAULT now(),
  last_used_at timestamptz
);

CREATE TABLE IF NOT EXISTS system_settings (
  key text PRIMARY KEY,
  value jsonb NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS audit_events (
  id bigserial PRIMARY KEY,
  actor text NOT NULL DEFAULT 'local-admin',
  action text NOT NULL,
  resource_type text NOT NULL,
  resource_id text,
  payload jsonb NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS config_versions (
  version bigserial PRIMARY KEY,
  status text NOT NULL CHECK (status IN ('draft','published','rolled_back')),
  checksum text NOT NULL,
  snapshot jsonb NOT NULL,
  errors jsonb NOT NULL DEFAULT '[]',
  published_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  source_version bigint REFERENCES config_versions(version)
);

CREATE TABLE IF NOT EXISTS gateway_config_acks (
  id bigserial PRIMARY KEY,
  gateway_id text NOT NULL,
  version bigint NOT NULL REFERENCES config_versions(version),
  checksum text NOT NULL,
  status text NOT NULL CHECK (status IN ('adopted','rejected')),
  error text,
  acknowledged_at timestamptz NOT NULL DEFAULT now()
);
