INSERT INTO providers (slug, name, adapter, base_url, enabled, metadata)
VALUES ('openai-codex', 'OpenAI Codex', 'openai-codex', 'https://chatgpt.com/backend-api/codex', true, '{"managed":true}'::jsonb)
ON CONFLICT (slug) DO UPDATE SET
  name = EXCLUDED.name,
  adapter = EXCLUDED.adapter,
  base_url = EXCLUDED.base_url,
  enabled = true,
  updated_at = now();
