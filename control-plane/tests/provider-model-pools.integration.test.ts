import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import { Pool } from 'pg';
import { ModelSchema } from '../lib/control.ts';
import { insertSecret } from '../lib/control.ts';
import { compileDeclarativeSnapshot } from '../lib/snapshots.ts';

const url = process.env.TEST_DATABASE_URL;
const options = { skip: url ? false : 'TEST_DATABASE_URL is not set' };
const schema = `provider_model_pools_${process.pid}`;
const pool = url ? new Pool({ connectionString: `${url}?options=-c%20search_path%3D${schema},public`, max: 2 }) : null;
const providerId = '00000000-0000-4000-8000-000000000001';

async function applyMigrations() {
  await pool!.query(`DROP SCHEMA IF EXISTS ${schema} CASCADE; CREATE SCHEMA ${schema}`);
  for (const name of (await fs.readdir(path.join(process.cwd(), 'migrations'))).filter(name => name.endsWith('.sql')).sort()) {
    const sql = (await fs.readFile(path.join(process.cwd(), 'migrations', name), 'utf8')).replace('CREATE EXTENSION IF NOT EXISTS pgcrypto;', '');
    await pool!.query(sql);
  }
}

test.before(async () => { if (url) await applyMigrations(); });

test('provider model migration preserves manual routes and constrains strategies', options, async () => {
  const provider = await pool!.query("INSERT INTO providers (id,slug,name,adapter,base_url) VALUES ($1,'pool-provider','Pool Provider','openai-compatible','https://api.example.test/v1') RETURNING id", [providerId]);
  await pool!.query("INSERT INTO model_aliases (alias,upstream_model) VALUES ('legacy-model','legacy-upstream')");
  const legacy = await pool!.query("SELECT provider_id,routing_strategy FROM model_aliases WHERE alias='legacy-model'");
  assert.deepEqual(legacy.rows[0], { provider_id: null, routing_strategy: 'manual' });
  await assert.rejects(
    pool!.query("INSERT INTO model_aliases (alias,upstream_model,provider_id,routing_strategy) VALUES ('bad','bad',$1,'random')", [provider.rows[0].id]),
    /check constraint/i,
  );
});

test('model validation separates manual and provider-backed routes', () => {
  assert.deepEqual(ModelSchema.parse({
    alias: 'luna', upstream_model: 'gpt-5.6-luna', provider_id: providerId, routing_strategy: 'round_robin',
  }), {
    alias: 'luna', upstream_model: 'gpt-5.6-luna', provider_id: providerId, routing_strategy: 'round_robin', enabled: true, fallbacks: [], input_per_mtok: 0, output_per_mtok: 0,
  });
  assert.deepEqual(ModelSchema.parse({ alias: 'manual', upstream_model: 'manual-upstream', accounts: [providerId] }), {
    alias: 'manual', upstream_model: 'manual-upstream', accounts: [providerId], routing_strategy: 'manual', enabled: true, fallbacks: [], input_per_mtok: 0, output_per_mtok: 0,
  });
  assert.throws(() => ModelSchema.parse({ alias: 'bad', upstream_model: 'bad', provider_id: providerId, routing_strategy: 'manual' }));
  assert.throws(() => ModelSchema.parse({ alias: 'bad', upstream_model: 'bad', routing_strategy: 'round_robin' }));
});

test('provider-backed snapshots dynamically resolve only available accounts from their provider', options, async () => {
  await pool!.query("UPDATE model_aliases SET enabled=false WHERE alias='legacy-model'");
  const otherProvider = '00000000-0000-4000-8000-000000000002';
  await pool!.query("INSERT INTO providers (id,slug,name,adapter,base_url) VALUES ($1,'pool-other','Pool Other','openai-compatible','https://other.example.test/v1')", [otherProvider]);
  const accountIds: string[] = [];
  for (const [index, provider] of [providerId, providerId, otherProvider].entries()) {
    const secret = await insertSecret(pool! as any, 'api_key', { api_key: `test-key-${index}` });
    const account = await pool!.query('INSERT INTO accounts (provider_id,secret_record_id,name,display_name,base_url,enabled,priority) VALUES ($1,$2,$3,$4,$5,true,$6) RETURNING id', [provider, secret, `pool-account-${index}`, `Pool Account ${index}`, 'https://api.example.test/v1', index + 1]);
    accountIds.push(account.rows[0].id);
    await pool!.query('INSERT INTO discovered_models (provider_id,account_id,upstream_model,display_name,available) VALUES ($1,$2,$3,$3,true)', [provider, account.rows[0].id, 'shared-model']);
  }
  await pool!.query("INSERT INTO model_aliases (alias,upstream_model,provider_id,routing_strategy) VALUES ('shared-public','shared-model',$1,'round_robin')", [providerId]);
  await pool!.query("INSERT INTO routing_settings (id,strategy,sticky_ttl,max_attempts) VALUES (true,'balanced','1h',2) ON CONFLICT (id) DO NOTHING");
  await pool!.query("INSERT INTO virtual_keys (name,key_hash,key_prefix,models,rpm) VALUES ('pool-vk',$1,'sk-pool',ARRAY['shared-public'],60)", ['e'.repeat(64)]);

  const first = await compileDeclarativeSnapshot(pool! as any);
  assert.deepEqual(first.snapshot.models.find((model: any) => model.alias === 'shared-public'), { alias: 'shared-public', upstream_model: 'shared-model', accounts: ['pool-account-0', 'pool-account-1'], routing_strategy: 'round_robin' });
  await pool!.query('UPDATE discovered_models SET available=false WHERE account_id=$1', [accountIds[1]]);
  const second = await compileDeclarativeSnapshot(pool! as any);
  assert.deepEqual(second.snapshot.models.find((model: any) => model.alias === 'shared-public').accounts, ['pool-account-0']);
});

test.after(async () => {
  if (pool) {
    await pool.query(`DROP SCHEMA IF EXISTS ${schema} CASCADE`);
    await pool.end();
  }
});
