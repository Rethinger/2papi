ALTER TABLE accounts ADD COLUMN IF NOT EXISTS auth_type text NOT NULL DEFAULT 'api_key';
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS credential_revision bigint NOT NULL DEFAULT 1 CHECK (credential_revision > 0);

ALTER TABLE config_versions DROP CONSTRAINT IF EXISTS config_versions_status_check;
ALTER TABLE config_versions ADD CONSTRAINT config_versions_status_check CHECK (status IN ('draft','published','rolled_back','invalid'));
ALTER TABLE config_versions ADD COLUMN IF NOT EXISTS schema_version integer NOT NULL DEFAULT 1;
ALTER TABLE config_versions ADD COLUMN IF NOT EXISTS config_checksum text;

CREATE TABLE IF NOT EXISTS snapshot_migration_state (
  migration text PRIMARY KEY,
  completed_at timestamptz NOT NULL DEFAULT now(),
  result jsonb NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS gateway_instances (
  gateway_id text PRIMARY KEY,
  supported_schemas integer[] NOT NULL DEFAULT '{1}',
  envelope_version integer NOT NULL DEFAULT 1,
  last_seen_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE gateway_config_acks ADD COLUMN IF NOT EXISTS schema_version integer NOT NULL DEFAULT 1;
ALTER TABLE gateway_config_acks ADD COLUMN IF NOT EXISTS config_checksum text;
ALTER TABLE gateway_config_acks ADD COLUMN IF NOT EXISTS credential_digest text;
ALTER TABLE gateway_config_acks ADD COLUMN IF NOT EXISTS runtime_checksum text;
ALTER TABLE gateway_config_acks ADD COLUMN IF NOT EXISTS envelope_version integer NOT NULL DEFAULT 1;
