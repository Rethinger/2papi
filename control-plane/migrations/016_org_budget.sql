-- E2 (Enterprise): organization budget caps every team under the org.
-- Semantics (docs/build-spine-specs.md, шаг 3): effective team budget =
-- min(team.budget_usd if > 0, org.budget_usd if > 0). 0 = unlimited.
-- Gateway receives the cap inside the team payload of the snapshot
-- (team.org), enforcement lives in internal/policy.

ALTER TABLE organizations ADD COLUMN IF NOT EXISTS budget_usd numeric(12,2) NOT NULL DEFAULT 0;
