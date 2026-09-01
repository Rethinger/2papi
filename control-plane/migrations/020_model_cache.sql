-- G4 gap: per-model exact-match response cache.
-- cache: '' (inherit; off unless X-Gateway-Cache:true) | 'off' | 'exact'
-- (non-streaming responses cached without a client header).
-- cache_ttl: Go duration string overriding the default 5m window.
ALTER TABLE model_aliases ADD COLUMN IF NOT EXISTS cache text NOT NULL DEFAULT '';
ALTER TABLE model_aliases ADD COLUMN IF NOT EXISTS cache_ttl text NOT NULL DEFAULT '';