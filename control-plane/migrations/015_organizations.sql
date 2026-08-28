-- E2 (Enterprise): organizations layer above teams + extended member roles.
-- Design: docs/strategy-v3.md (Enterprise-гейтинг, п.2); LiteLLM-якорь:
-- org_admin/team_admin — премиум-роли над командами.

CREATE TABLE IF NOT EXISTS organizations (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name text NOT NULL,
  owner_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS organizations_name_folded_uq
  ON organizations (lower(name));

ALTER TABLE teams ADD COLUMN IF NOT EXISTS org_id uuid REFERENCES organizations(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS teams_org_idx ON teams (org_id);

-- team_members.role: add org/team admin roles. The CHECK constraint in
-- 011 was inline (unnamed) — Postgres auto-named it team_members_role_check.
ALTER TABLE team_members DROP CONSTRAINT IF EXISTS team_members_role_check;
ALTER TABLE team_members ADD CONSTRAINT team_members_role_check
  CHECK (role IN ('owner','member','org_admin','team_admin'));
