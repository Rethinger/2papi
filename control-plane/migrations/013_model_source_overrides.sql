-- E-cycle: multi-provider aliases — per-source overrides on the mapping.
-- Design: plan/loop-2papi-research-state.md (cycle E).
-- One public alias may be served by many providers; each source may use
-- its own upstream model id and its own prices. Empty override = inherit
-- the alias-level upstream_model / costs (back-compat).

ALTER TABLE model_account_mappings
  ADD COLUMN IF NOT EXISTS upstream_model_override text NOT NULL DEFAULT '';

ALTER TABLE model_account_mappings
  ADD COLUMN IF NOT EXISTS weight integer;

ALTER TABLE model_account_mappings
  ADD COLUMN IF NOT EXISTS input_cost_per_mtok numeric(12,6);

ALTER TABLE model_account_mappings
  ADD COLUMN IF NOT EXISTS output_cost_per_mtok numeric(12,6);
