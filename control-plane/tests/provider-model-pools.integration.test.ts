import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import { Pool } from 'pg';
import { ModelSchema } from '../lib/control.ts';

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
    alias: 'luna', upstream_model: 'gpt-5.6-luna', provider_id: providerId, routing_strategy: 'round_robin', enabled: true,
  });
  assert.deepEqual(ModelSchema.parse({ alias: 'manual', upstream_model: 'manual-upstream', accounts: [providerId] }), {
    alias: 'manual', upstream_model: 'manual-upstream', accounts: [providerId], routing_strategy: 'manual', enabled: true,
  });
  assert.throws(() => ModelSchema.parse({ alias: 'bad', upstream_model: 'bad', provider_id: providerId, routing_strategy: 'manual' }));
  assert.throws(() => ModelSchema.parse({ alias: 'bad', upstream_model: 'bad', routing_strategy: 'round_robin' }));
});

test.after(async () => {
  if (pool) {
    await pool.query(`DROP SCHEMA IF EXISTS ${schema} CASCADE`);
    await pool.end();
  }
});
