-- MCP servers managed from the control-plane dashboard (OSS feature).
-- Upstream credentials live encrypted in secret_records (purpose
-- 'mcp_headers'); declarative snapshots carry only name/url/enabled and the
-- runtime materialization injects decrypted headers — same split as account
-- credentials.

CREATE TABLE IF NOT EXISTS mcp_servers (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name text NOT NULL,
  url text NOT NULL,
  enabled boolean NOT NULL DEFAULT true,
  headers_secret_id uuid REFERENCES secret_records(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS mcp_servers_name_folded_uq ON mcp_servers (lower(name));
