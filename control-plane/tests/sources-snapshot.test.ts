import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import { Pool } from 'pg';
import { compileDeclarativeSnapshot } from '../lib/snapshots.ts';

// Шаг 5 хребта: per-account source overrides ride on the model entry only
// when a mapping actually overrides something; legacy 1:1 stays unchanged.

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
  // The snapshot compiler refuses to build without an enabled key.
  await pool.query(
    `INSERT INTO virtual_keys (name, key_hash, key_prefix, models, rpm) VALUES ('seed-key', $1, 'sk-seed', ARRAY['model-a'], 60)`,
    ['a'.repeat(64)],
  );
});

test.after(async () => {
  await pool?.end();
});

async function seedModelWithSources(alias: string, overrides: Record<string, unknown> | null) {
  const provider = (await pool!.query(
    `INSERT INTO providers (slug,name,adapter,base_url) VALUES ($1,$1,'openai-compatible','https://up.test/v1') RETURNING id`,
    [`p-${alias}`],
  )).rows[0];
  const accountA = (await pool!.query(
    `INSERT INTO accounts (provider_id,name,display_name,base_url,enabled) VALUES ($1,$2,$2,'https://up.test/v1',true) RETURNING id`,
    [provider.id, `acc-${alias}-a`],
  )).rows[0];
  const accountB = (await pool!.query(
    `INSERT INTO accounts (provider_id,name,display_name,base_url,enabled) VALUES ($1,$2,$2,'https://up.test/v1',true) RETURNING id`,
    [provider.id, `acc-${alias}-b`],
  )).rows[0];
  const model = (await pool!.query(
    `INSERT INTO model_aliases (alias,upstream_model,routing_strategy,enabled,fallbacks) VALUES ($1,'default-upstream','manual',true,'{}') RETURNING id`,
    [alias],
  )).rows[0];
  await pool!.query(`INSERT INTO model_account_mappings (model_alias_id,account_id,position) VALUES ($1,$2,0),($1,$3,1)`, [model.id, accountA.id, accountB.id]);
  if (overrides) {
    await pool!.query(
      `UPDATE model_account_mappings SET upstream_model_override=COALESCE($2,''), weight=$3, input_cost_per_mtok=$4, output_cost_per_mtok=$5
       WHERE model_alias_id=$1 AND account_id=(SELECT id FROM accounts WHERE name=$6)`,
      [model.id, overrides.upstream_model_override ?? null, overrides.weight ?? null, overrides.input_cost_per_mtok ?? null, overrides.output_cost_per_mtok ?? null, overrides.account_name],
    );
  }
}

async function snapshotModels() {
  const client = await pool!.connect();
  try {
    return (await compileDeclarativeSnapshot(client)).snapshot.models;
  } finally {
    client.release();
  }
}

test('sources are emitted when mappings override, absent otherwise', options, async () => {
  // No overrides anywhere: legacy shape.
  await seedModelWithSources('plain', null);
  let models = await snapshotModels();
  assert.equal(models.find((m: any) => m.alias === 'plain').sources, undefined);
  assert.deepEqual(models.find((m: any) => m.alias === 'plain').accounts.sort(), ['acc-plain-a', 'acc-plain-b']);

  await seedModelWithSources('multi', {
    account_name: 'acc-multi-b',
    upstream_model_override: 'zhipu-glm-5',
    weight: 7,
    input_cost_per_mtok: 0.11,
    output_cost_per_mtok: 0.44,
  });
  models = await snapshotModels();

  const multi = models.find((m: any) => m.alias === 'multi');
  assert.equal(multi.upstream_model, 'default-upstream', 'alias default untouched');
  assert.deepEqual(multi.sources, [{ account: 'acc-multi-b', upstream_model: 'zhipu-glm-5', weight: 7, input_cost_per_mtok: 0.11, output_cost_per_mtok: 0.44 }]);

  const plain = models.find((m: any) => m.alias === 'plain');
  assert.equal(plain.sources, undefined, 'untouched alias keeps the legacy shape');
});
