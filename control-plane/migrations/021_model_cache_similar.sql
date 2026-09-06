-- Third cache mode: 'similar' serves a near-duplicate answer when the exact key
-- misses (Jaccard overlap on the last user message, single-turn requests only).
-- cache: '' (inherit) | 'off' | 'exact' | 'similar'
--
-- cache_similar_threshold is NULLABLE on purpose, and that is not cosmetic: the
-- gateway rejects an explicit 0 as a config error, so "no threshold chosen" has
-- to be a different value from any number a user could type. NULL means "use the
-- gateway default" (0.95); a stored 0 would look like a configured mode that is
-- silently switched off.
ALTER TABLE model_aliases ADD COLUMN IF NOT EXISTS cache_similar_threshold double precision;
