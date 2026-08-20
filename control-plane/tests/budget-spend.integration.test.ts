import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import { Pool } from 'pg';
import { storeRequestEvents } from '../lib/request-events.ts';
import { compileDeclarativeSnapshot } from '../lib/snapshots.ts';

const url = process.env.TEST_DATABASE_URL;
const options = { skip: url ? false : 'TEST_DATABASE_URL is not set' };
const pool = url ? new Pool({ connectionString: url, max: 2 }) : null;

test.before(async () => {
  if (!pool) return;
  await pool.query('DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;');
  const dir = path.join(process.cwd(), 'migrations');
  for (const name of (await fs.readdir(dir)).filter(name => name.endsWith('.sql')).sort()) {
    await pool.query(await fs.readFile(path.join(dir, name), 'utf8'));
  }
});

test.after(async () => {
  await pool?.end();
});

test('storeRequestEvents rolls up daily spend ledger per virtual key', options, async () => {
  const vk = (await pool!.query(`INSERT INTO virtual_keys (name, key_hash, key_prefix, rpm, tpm, max_concurrency, budget_usd)
    VALUES ('spend-test-vk', ${'0'.repeat(64)}, 'sk-spend', 60, 1000, 2, 10.0) RETURNING id`)).rows[0];

  const now = new Date().toISOString();
  const batch = {
    gateway_id: 'gw-ledger',
    events: [
      {
        request_id: '11111111111111111111111111111101',
        occurred_at: now,
        endpoint: '/v1/chat/completions' as const,
        public_model: 'model-a',
        upstream_model: 'up-a',
        virtual_key: 'sk-spend',
        virtual_key_id: vk.id,
        streaming: false,
        config_version: 1,
        final_status: 200,
        success: true,
        total_latency_ms: 50,
        input_tokens: 100,
        output_tokens: 50,
        total_tokens: 150,
        cost_usd: 0.0015,
        attempts: [{
          account: 'primary',
          adapter: 'openai-compatible',
          alias: 'model-a',
          status: 200,
          outcome: 'success' as const,
          latency_ms: 45,
          cooldown_ms: 0,
        }],
      },
      {
        request_id: '11111111111111111111111111111102',
        occurred_at: now,
        endpoint: '/v1/chat/completions' as const,
        public_model: 'model-a',
        upstream_model: 'up-a',
        virtual_key: 'sk-spend',
        virtual_key_id: vk.id,
        streaming: false,
        config_version: 1,
        final_status: 200,
        success: true,
        total_latency_ms: 60,
        input_tokens: 200,
        output_tokens: 100,
        total_tokens: 300,
        cost_usd: 0.0030,
        attempts: [{
          account: 'primary',
          adapter: 'openai-compatible',
          alias: 'model-a',
          status: 200,
          outcome: 'success' as const,
          latency_ms: 55,
          cooldown_ms: 0,
        }],
      },
    ],
  };

  const stored = await storeRequestEvents(pool!, batch.gateway_id, batch.events);
  assert.equal(stored.inserted, 2);

  const spendRow = (await pool!.query(
    `SELECT cost_usd, tokens_in, tokens_out, requests FROM key_spend_daily WHERE virtual_key_id = $1`,
    [vk.id],
  )).rows[0];

  assert.ok(spendRow, 'daily spend row must be present');
  assert.equal(Number(spendRow.cost_usd), 0.0045);
  assert.equal(Number(spendRow.tokens_in), 300);
  assert.equal(Number(spendRow.tokens_out), 150);
  assert.equal(Number(spendRow.requests), 2);

  // Idempotent re-delivery must not double-accumulate spend
  const reStored = await storeRequestEvents(pool!, batch.gateway_id, batch.events);
  assert.equal(reStored.inserted, 0);

  const spendRowAfter = (await pool!.query(
    `SELECT cost_usd, requests FROM key_spend_daily WHERE virtual_key_id = $1`,
    [vk.id],
  )).rows[0];
  assert.equal(Number(spendRowAfter.cost_usd), 0.0045);
  assert.equal(Number(spendRowAfter.requests), 2);
});

test('compileDeclarativeSnapshot emits pricing, fallbacks, and limits into snapshot', options, async () => {
  const prov = (await pool!.query(`INSERT INTO providers (slug, name, adapter, base_url, enabled)
    VALUES ('snap-prov', 'Snap Prov', 'openai-compatible', 'http://fake:9001', true) RETURNING id`)).rows[0];
  const acct = (await pool!.query(`INSERT INTO accounts (provider_id, name, display_name, base_url, enabled, priority, weight, max_concurrency, cost)
    VALUES ($1, 'snap-acct', 'Snap Acct', 'http://fake:9001', true, 1, 1, 100, 0) RETURNING id`, [prov.id])).rows[0];

  const m2 = (await pool!.query(`INSERT INTO model_aliases (alias, upstream_model, enabled, fallbacks)
    VALUES ('snap-fallback', 'up-fb', true, '{}') RETURNING id`)).rows[0];
  await pool!.query(`INSERT INTO model_account_mappings (model_alias_id, account_id, position) VALUES ($1, $2, 0)`, [m2.id, acct.id]);
  const m1 = (await pool!.query(`INSERT INTO model_aliases (alias, upstream_model, enabled, fallbacks)
    VALUES ('snap-primary', 'up-prim', true, ARRAY['snap-fallback']) RETURNING id`)).rows[0];
  await pool!.query(`INSERT INTO model_account_mappings (model_alias_id, account_id, position) VALUES ($1, $2, 0)`, [m1.id, acct.id]);
  await pool!.query(`INSERT INTO model_pricing (model_alias_id, input_per_mtok, output_per_mtok)
    VALUES ($1, 0.15, 0.60)`, [m1.id]);

  const client = await pool!.connect();
  try {
    const compiled = await compileDeclarativeSnapshot(client);
    assert.equal(compiled.schemaVersion, 2);

    type SnapshotModel = { alias: string; input_cost_per_mtok?: number; output_cost_per_mtok?: number; fallbacks?: string[] };
    type SnapshotVk = { name: string; budget_usd?: number; tpm?: number; max_concurrency?: number };
    const models = compiled.snapshot.models as SnapshotModel[];
    const snapM1 = models.find(m => m.alias === 'snap-primary');
    assert.ok(snapM1);
    assert.equal(snapM1.input_cost_per_mtok, 0.15);
    assert.equal(snapM1.output_cost_per_mtok, 0.60);
    assert.deepEqual(snapM1.fallbacks, ['snap-fallback']);

    const snapM2 = models.find(m => m.alias === 'snap-fallback');
    assert.ok(snapM2);
    assert.equal(snapM2.input_cost_per_mtok, undefined);
    assert.equal(snapM2.fallbacks, undefined);

    const vks = compiled.snapshot.virtual_keys as SnapshotVk[];
    const snapVk = vks.find(k => k.name === 'spend-test-vk');
    assert.ok(snapVk);
    assert.equal(snapVk.budget_usd, 10.0);
    assert.equal(snapVk.tpm, 1000);
    assert.equal(snapVk.max_concurrency, 2);
  } finally {
    client.release();
  }
});

test('compileDeclarativeSnapshot rejects unknown fallbacks and cycles', options, async () => {
  const prov = (await pool!.query("SELECT id FROM providers WHERE slug='snap-prov'")).rows[0];
  const acct = (await pool!.query("SELECT id FROM accounts WHERE name='snap-acct'")).rows[0];

  // 1. Unknown fallback alias
  const badModel = (await pool!.query(`INSERT INTO model_aliases (alias, upstream_model, enabled, fallbacks)
    VALUES ('bad-fb-model', 'up-bad', true, ARRAY['nonexistent-alias']) RETURNING id`)).rows[0];
  await pool!.query(`INSERT INTO model_account_mappings (model_alias_id, account_id, position) VALUES ($1, $2, 0)`, [badModel.id, acct.id]);

  const client = await pool!.connect();
  try {
    await assert.rejects(
      compileDeclarativeSnapshot(client),
      /model bad-fb-model fallback references unknown model nonexistent-alias/,
    );
  } finally {
    client.release();
  }

  // Fix the unknown reference to prepare cycle test
  await pool!.query(`UPDATE model_aliases SET fallbacks = '{}' WHERE id = $1`, [badModel.id]);

  // 2. Direct cycle: cycle-a -> cycle-b -> cycle-a
  const cycA = (await pool!.query(`INSERT INTO model_aliases (alias, upstream_model, enabled, fallbacks)
    VALUES ('cycle-a', 'up-cyc-a', true, ARRAY['cycle-b']) RETURNING id`)).rows[0];
  await pool!.query(`INSERT INTO model_account_mappings (model_alias_id, account_id, position) VALUES ($1, $2, 0)`, [cycA.id, acct.id]);

  const cycB = (await pool!.query(`INSERT INTO model_aliases (alias, upstream_model, enabled, fallbacks)
    VALUES ('cycle-b', 'up-cyc-b', true, ARRAY['cycle-a']) RETURNING id`)).rows[0];
  await pool!.query(`INSERT INTO model_account_mappings (model_alias_id, account_id, position) VALUES ($1, $2, 0)`, [cycB.id, acct.id]);

  const cycleClient = await pool!.connect();
  try {
    await assert.rejects(
      compileDeclarativeSnapshot(cycleClient),
      /model fallback cycle involving/,
    );
  } finally {
    cycleClient.release();
  }

  // Cleanup cycle models
  await pool!.query(`DELETE FROM model_aliases WHERE alias IN ('bad-fb-model', 'cycle-a', 'cycle-b')`);
});
