import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import { Pool } from 'pg';
import { deleteModelRoute, updateProviderModelStrategy } from '../lib/model-routes.ts';

const url = process.env.TEST_DATABASE_URL;
const options = { skip: url ? false : 'TEST_DATABASE_URL is not set' };
const schema = `model_routes_${process.pid}`;
const pool = url ? new Pool({ connectionString: `${url}?options=-c%20search_path%3D${schema},public`, max: 2 }) : null;

test.before(async () => {
  if (!pool) return;
  await pool.query(`DROP SCHEMA IF EXISTS ${schema} CASCADE; CREATE SCHEMA ${schema}`);
  for (const name of (await fs.readdir(path.join(process.cwd(), 'migrations'))).filter(name => name.endsWith('.sql')).sort()) {
    await pool.query((await fs.readFile(path.join(process.cwd(), 'migrations', name), 'utf8')).replace('CREATE EXTENSION IF NOT EXISTS pgcrypto;', ''));
  }
});

async function seed() {
  const provider = await pool!.query("INSERT INTO providers (slug,name,adapter,base_url) VALUES ('model-route-provider','Model Route Provider','openai-compatible','https://api.example.test/v1') RETURNING id");
  const account = await pool!.query("INSERT INTO accounts (provider_id,name,display_name,base_url) VALUES ($1,'model-route-account','Model Route Account','https://api.example.test/v1') RETURNING id", [provider.rows[0].id]);
  await pool!.query("INSERT INTO discovered_models (provider_id,account_id,upstream_model,display_name) VALUES ($1,$2,'route-upstream','Route Upstream')", [provider.rows[0].id, account.rows[0].id]);
  const target = await pool!.query("INSERT INTO model_aliases (alias,upstream_model,provider_id,routing_strategy) VALUES ('route-target','route-upstream',$1,'round_robin') RETURNING id", [provider.rows[0].id]);
  const survivor = await pool!.query("INSERT INTO model_aliases (alias,upstream_model) VALUES ('route-survivor','route-upstream') RETURNING id");
  await pool!.query('INSERT INTO model_account_mappings (model_alias_id,account_id) VALUES ($1,$2)', [survivor.rows[0].id, account.rows[0].id]);
  await pool!.query("INSERT INTO routing_settings (id,strategy,sticky_ttl,max_attempts) VALUES (true,'balanced','1h',2) ON CONFLICT (id) DO NOTHING");
  await pool!.query("INSERT INTO virtual_keys (name,key_hash,key_prefix,models,rpm) VALUES ('route-vk-a',$1,'sk-route-a',ARRAY['route-target','route-survivor'],60),('route-vk-b',$2,'sk-route-b',ARRAY['route-target'],60)", ['a'.repeat(64), 'b'.repeat(64)]);
  return { targetId: target.rows[0].id, providerId: provider.rows[0].id };
}

test('hard deletion removes the public route and key references but keeps discovery evidence', options, async () => {
  const seeded = await seed();
  const result = await deleteModelRoute(pool! as any, seeded.targetId);
  assert.deepEqual(result, { id: seeded.targetId, alias: 'route-target', deleted: true });
  assert.equal((await pool!.query('SELECT count(*)::int n FROM model_aliases WHERE id=$1', [seeded.targetId])).rows[0].n, 0);
  assert.equal((await pool!.query("SELECT count(*)::int n FROM discovered_models WHERE upstream_model='route-upstream'")).rows[0].n, 1);
  assert.deepEqual((await pool!.query('SELECT models FROM virtual_keys ORDER BY name')).rows.map(row => row.models), [['route-survivor'], []]);
  assert.equal((await pool!.query("SELECT count(*)::int n FROM audit_events WHERE action='delete' AND resource_type='model_alias'")).rows[0].n, 1);
  assert.equal((await pool!.query("SELECT count(*)::int n FROM config_versions WHERE status='draft'")).rows[0].n, 1);
  await assert.rejects(() => deleteModelRoute(pool! as any, seeded.targetId), (error: any) => error.status === 404);
});

test('provider route strategy updates are restricted and create a draft', options, async () => {
  await pool!.query('TRUNCATE audit_events,config_versions,model_account_mappings,model_aliases,discovered_models,accounts,providers,virtual_keys CASCADE');
  const seeded = await seed();
  const updated = await updateProviderModelStrategy(pool! as any, seeded.targetId, 'quota_failover');
  assert.equal(updated.routing_strategy, 'quota_failover');
  await assert.rejects(() => updateProviderModelStrategy(pool! as any, seeded.targetId, 'manual' as any), (error: any) => error.status === 400);
});

test.after(async () => { if (pool) { await pool.query(`DROP SCHEMA IF EXISTS ${schema} CASCADE`); await pool.end(); } });
