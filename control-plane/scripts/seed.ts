import crypto from 'node:crypto';
import { pool, tx } from '../lib/db';
import { audit, insertSecret, publishLatest, storeDraft } from '../lib/control';

await tx(async c => {
  const count = await c.query('SELECT count(*)::int n FROM providers');
  if (count.rows[0].n > 0) {
    const latest = await c.query("SELECT 1 FROM config_versions WHERE status='published' LIMIT 1");
    if (!latest.rows[0]) {
      await storeDraft(c);
      await publishLatest(c);
    }
    return;
  }
  const provider = await c.query("INSERT INTO providers (slug,name,adapter,base_url) VALUES ('generic-openai','Generic OpenAI Compatible','openai-compatible','http://fake-upstream:9001') RETURNING id");
  const s1 = await insertSecret(c, 'account_credential', { api_key: 'upstream-primary' });
  const s2 = await insertSecret(c, 'account_credential', { api_key: 'upstream-secondary' });
  const a1 = await c.query("INSERT INTO accounts (provider_id,secret_record_id,name,display_name,base_url,priority,weight,cost) VALUES ($1,$2,'primary','Primary','http://fake-upstream:9001',1,2,0.15) RETURNING id", [provider.rows[0].id, s1]);
  const a2 = await c.query("INSERT INTO accounts (provider_id,secret_record_id,name,display_name,base_url,priority,weight,cost) VALUES ($1,$2,'secondary','Secondary','http://fake-upstream:9002',2,1,0.20) RETURNING id", [provider.rows[0].id, s2]);
  const model = await c.query("INSERT INTO model_aliases (alias,upstream_model) VALUES ('gpt-dev','gpt-4o-mini') RETURNING id");
  await c.query('INSERT INTO model_account_mappings (model_alias_id,account_id,position) VALUES ($1,$2,0),($1,$3,1)', [model.rows[0].id, a1.rows[0].id, a2.rows[0].id]);
  await c.query("INSERT INTO routing_settings (id,strategy,sticky_ttl,max_attempts) VALUES (true,'balanced','1h',2) ON CONFLICT DO NOTHING");
  const key = 'sk-gateway-dev';
  const hash = crypto.createHmac('sha256', process.env.GATEWAY_SHARED_SECRET ?? 'dev-secret-change-me').update(key).digest('hex');
  await c.query("INSERT INTO virtual_keys (name,key_hash,key_prefix,models,rpm) VALUES ('dev',$1,'sk-gateway',ARRAY['gpt-dev'],60)", [hash]);
  await c.query("INSERT INTO system_settings (key,value) VALUES ('bind_host','\"127.0.0.1\"'::jsonb),('phase','\"A\"'::jsonb)");
  await audit(c, 'seed', 'database', undefined, { provider: 'generic-openai', accounts: 2, model: 'gpt-dev' });
  await storeDraft(c);
  await publishLatest(c);
});
await pool.end();
console.log('seed complete');
