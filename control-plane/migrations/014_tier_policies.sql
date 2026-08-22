-- B-cycle: free tier policies — per-model daily token limits.
-- Design: plan/2papi-cloud-research.md (подцикл B, фри-план v2).
-- Operator edits these from the dashboard; changes publish a new config
-- snapshot. The gateway enforces the daily token bucket per tier+cluster.

CREATE TABLE IF NOT EXISTS tier_policies (
  tier text NOT NULL,
  model_alias_id uuid NOT NULL REFERENCES model_aliases(id) ON DELETE CASCADE,
  tokens_per_day bigint NOT NULL CHECK (tokens_per_day > 0),
  enabled boolean NOT NULL DEFAULT true,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tier, model_alias_id)
);

CREATE INDEX IF NOT EXISTS tier_policies_tier_idx ON tier_policies (tier) WHERE enabled;
