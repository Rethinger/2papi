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

test('team budget flows into snapshot and spend ledger', options, async () => {
  const team = (await pool!.query(`INSERT INTO teams (name, budget_usd) VALUES ('platform', 25.0) RETURNING id`)).rows[0];
  const vk = (await pool!.query(`INSERT INTO virtual_keys (name, key_hash, key_prefix, models, rpm, team_id)
    VALUES ('platform-key', '${'a'.repeat(64)}', 'sk-platform', ARRAY['model-a'], 60, $1) RETURNING id`, [team.id])).rows[0];

  // Snapshot emits the shared team budget on the virtual key
  const client = await pool!.connect();
  let snapKey: any;
  try {
    const compiled = await compileDeclarativeSnapshot(client);
    snapKey = compiled.snapshot.virtual_keys.find((k: any) => k.name === 'platform-key');
  } finally {
    client.release();
  }
  assert.ok(snapKey, 'key should be in snapshot');
  assert.deepEqual(snapKey.team, { id: team.id, budget_usd: 25.0, share_usd: 25.0 }, 'single key owns the whole fair share');

  // Spend recorded against the key aggregates into the team's daily ledger
  await storeRequestEvents(pool!, 'gw-teams', [{
    request_id: '99999999999999999999999999999999',
    occurred_at: new Date().toISOString(),
    endpoint: '/v1/chat/completions',
    public_model: 'model-a',
    upstream_model: 'up-a',
    virtual_key: 'platform-key',
    virtual_key_id: vk.id,
    streaming: false,
    config_version: 1,
    final_status: 200,
    success: true,
    total_latency_ms: 40,
    input_tokens: 100,
    output_tokens: 50,
    total_tokens: 150,
    cost_usd: 0.0020,
    attempts: [{
      account: 'primary',
      adapter: 'openai-compatible',
      alias: 'model-a',
      status: 200,
      outcome: 'success',
      latency_ms: 35,
      cooldown_ms: 0,
    }],
  }]);

  const spend = (await pool!.query(
    `SELECT COALESCE(sum(ksd.cost_usd),0) cost FROM key_spend_daily ksd
     JOIN virtual_keys vk ON vk.id=ksd.virtual_key_id WHERE vk.team_id=$1`,
    [team.id],
  )).rows[0];
  assert.equal(Number(spend.cost), 0.002);
});
