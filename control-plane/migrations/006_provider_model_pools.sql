ALTER TABLE model_aliases
  ADD COLUMN IF NOT EXISTS provider_id uuid REFERENCES providers(id) ON DELETE SET NULL;

ALTER TABLE model_aliases
  ADD COLUMN IF NOT EXISTS routing_strategy text NOT NULL DEFAULT 'manual';

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'model_aliases_routing_strategy_check'
  ) THEN
    ALTER TABLE model_aliases
      ADD CONSTRAINT model_aliases_routing_strategy_check
      CHECK (routing_strategy IN ('manual', 'round_robin', 'quota_failover'));
  END IF;
END $$;

CREATE INDEX IF NOT EXISTS model_aliases_provider_upstream_idx
  ON model_aliases(provider_id, upstream_model)
  WHERE provider_id IS NOT NULL;
