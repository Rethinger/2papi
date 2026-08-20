-- P3: multi-tenant teams with shared daily budgets across their virtual keys.

CREATE TABLE IF NOT EXISTS teams (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name text NOT NULL,
  enabled boolean NOT NULL DEFAULT true,
  budget_usd numeric(12,6) NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS teams_name_folded_uq ON teams (lower(name));

ALTER TABLE virtual_keys ADD COLUMN IF NOT EXISTS team_id uuid REFERENCES teams(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS virtual_keys_team_idx ON virtual_keys (team_id);
