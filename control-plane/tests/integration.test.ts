import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import { Pool } from 'pg';
import { audit, compileSnapshot, insertSecret, publishLatest, storeDraft } from '../lib/control.ts';
import { sanitizeHistoricalConfigVersions } from '../lib/snapshot-migration.ts';

const url = process.env.TEST_DATABASE_URL;
const options = { skip: url ? false : 'TEST_DATABASE_URL is not set' };
const pool = url ? new Pool({ connectionString: url, max: 4 }) : null;

test.before(async () => {
  if (!url) return;
  await pool!.query('DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;');
  await applyMigrations(pool!);
});

async function applyMigrations(client: any) {
  const dir = path.join(process.cwd(), 'migrations');
  for (const name of (await fs.readdir(dir)).filter(n => n.endsWith('.sql')).sort()) {
    await client.query(await fs.readFile(path.join(dir, name), 'utf8'));
  }
  await client.query('BEGIN');
  try { await sanitizeHistoricalConfigVersions(client); await client.query('COMMIT'); } catch (e) { await client.query('ROLLBACK'); throw e; }
}

async function withRollback<T>(fn: (client: any) => Promise<T>): Promise<T> {
  const client = await pool!.connect();
  try {
    await client.query('BEGIN');
    return await fn(client);
  } finally {
    await client.query('ROLLBACK');
    client.release();
  }
}

async function seedMinimal(client: any) {
  const provider = await client.query("INSERT INTO providers (slug,name,adapter,base_url) VALUES ('itest','Integration','openai-compatible','http://upstream:9001') RETURNING id");
  const secret = await insertSecret(client, 'account_credential', { api_key: 'integration-secret' });
  const account = await client.query(
    "INSERT INTO accounts (provider_id,secret_record_id,name,display_name,base_url) VALUES ($1,$2,'itest-primary','Integration Primary','http://upstream:9001') RETURNING id",
    [provider.rows[0].id, secret],
  );
  const model = await client.query("INSERT INTO model_aliases (alias,upstream_model) VALUES ('itest-model','gpt-4o-mini') RETURNING id");
  await client.query('INSERT INTO model_account_mappings (model_alias_id,account_id,position) VALUES ($1,$2,0)', [model.rows[0].id, account.rows[0].id]);
  await client.query("INSERT INTO routing_settings (id,strategy,sticky_ttl,max_attempts) VALUES (true,'balanced','1h',2) ON CONFLICT (id) DO NOTHING");
  await client.query("INSERT INTO virtual_keys (name,key_hash,key_prefix,models,rpm) VALUES ('itest-key',$1,'sk-itest',ARRAY['itest-model'],60)", ['b'.repeat(64)]);
  return { providerId: provider.rows[0].id, accountId: account.rows[0].id, secretId: secret };
}

test('migrations create every control-plane table', options, async () => {
  const names = ['providers', 'secret_records', 'accounts', 'model_aliases', 'model_account_mappings', 'routing_settings', 'virtual_keys', 'system_settings', 'audit_events', 'config_versions', 'gateway_config_acks'];
  const found = await pool!.query('SELECT table_name FROM information_schema.tables WHERE table_schema=$1', ['public']);
  const present = new Set(found.rows.map(row => row.table_name));
  for (const name of names) assert.ok(present.has(name), `missing table ${name}`);
});

test('migration runner is idempotent', options, async () => {
  await applyMigrations(pool!);
  const providers = await pool!.query('SELECT count(*)::int n FROM providers');
  assert.ok(providers.rows[0].n >= 0);
});

test('constraints reject invalid rows', options, async () => {
  await withRollback(async client => {
    await assert.rejects(
      client.query("INSERT INTO accounts (provider_id,name,display_name,base_url) VALUES (gen_random_uuid(),'orphan','Orphan','http://x')"),
      /foreign key/i,
    );
  });
  await withRollback(async client => {
    await assert.rejects(
      client.query("INSERT INTO config_versions (status,checksum,snapshot) VALUES ('bogus','abc','{}')"),
      /check constraint/i,
    );
  });
  await withRollback(async client => {
    const { providerId } = await seedMinimal(client);
    await assert.rejects(
      client.query("INSERT INTO accounts (provider_id,name,display_name,base_url,weight) VALUES ($1,'bad-weight','Bad','http://x',0)", [providerId]),
      /check constraint/i,
    );
  });
});

test('audit events persist inside the mutating transaction', options, async () => {
  await withRollback(async client => {
    const { providerId } = await seedMinimal(client);
    await audit(client, 'create', 'provider', providerId, { slug: 'itest' });
    const events = await client.query('SELECT action,resource_type,resource_id FROM audit_events WHERE resource_id=$1', [providerId]);
    assert.equal(events.rows.length, 1);
    assert.equal(events.rows[0].action, 'create');
    assert.equal(events.rows[0].resource_type, 'provider');
  });
});

test('credentials are unreadable in storage but decrypt during compilation', options, async () => {
  await withRollback(async client => {
    const { secretId } = await seedMinimal(client);
    const stored = await client.query('SELECT * FROM secret_records WHERE id=$1', [secretId]);
    const row = stored.rows[0];
    const blob = Buffer.concat([row.secret_ciphertext, row.data_key_ciphertext]).toString('utf8');
    assert.ok(!blob.includes('integration-secret'));

    const compiled = await compileSnapshot(client);
    const account = compiled.snapshot.accounts.find((item: any) => item.name === 'itest-primary');
    assert.ok(account, 'compiled snapshot is missing the seeded account');
    assert.equal(account.credential.api_key, 'integration-secret');
    assert.equal(compiled.checksum.length, 64);
  });
});

test('draft, publish, and rollback move the published pointer', options, async () => {
  await withRollback(async client => {
    await seedMinimal(client);
    const draft = await storeDraft(client);
    assert.equal(draft.status, 'draft');
    const first = await publishLatest(client);

    await client.query("UPDATE accounts SET weight=5 WHERE name='itest-primary'");
    await storeDraft(client);
    const second = await publishLatest(client);
    assert.ok(second.version > first.version);
    assert.notEqual(second.checksum, first.checksum);

    const source = await client.query('SELECT snapshot,checksum FROM config_versions WHERE version=$1', [first.version]);
    const clone = await client.query(
      'INSERT INTO config_versions (status,checksum,config_checksum,schema_version,snapshot,source_version) VALUES ($1,$2,$2,2,$3,$4) RETURNING version,checksum',
      ['draft', source.rows[0].checksum, JSON.stringify(source.rows[0].snapshot), first.version],
    );
    const restored = await publishLatest(client);
    assert.equal(restored.version, Number(clone.rows[0].version));
    assert.equal(restored.checksum, first.checksum);

    const latest = await client.query("SELECT version,checksum FROM config_versions WHERE status='published' ORDER BY version DESC LIMIT 1");
    assert.equal(latest.rows[0].checksum, first.checksum);
  });
});

test('gateway acknowledgements reference real versions', options, async () => {
  await withRollback(async client => {
    await seedMinimal(client);
    await storeDraft(client);
    const published = await publishLatest(client);
    await client.query(
      'INSERT INTO gateway_config_acks (gateway_id,version,checksum,status) VALUES ($1,$2,$3,$4)',
      ['gateway-itest', published.version, published.checksum, 'adopted'],
    );
    const acks = await client.query('SELECT status FROM gateway_config_acks WHERE gateway_id=$1', ['gateway-itest']);
    assert.equal(acks.rows[0].status, 'adopted');
    await assert.rejects(
      client.query("INSERT INTO gateway_config_acks (gateway_id,version,checksum,status) VALUES ('gateway-itest',999999,'x','adopted')"),
      /foreign key/i,
    );
  });
});

test('model account reassignment replaces mappings and reorders the compiled snapshot', options, async () => {
  await withRollback(async client => {
    const seeded = await seedMinimal(client);
    const model = await client.query("SELECT id FROM model_aliases WHERE alias='itest-model'");
    const modelId = model.rows[0].id;

    const secondSecret = await insertSecret(client, 'account_credential', { api_key: 'second-secret' });
    const second = await client.query(
      "INSERT INTO accounts (provider_id,secret_record_id,name,display_name,base_url) VALUES ($1,$2,'itest-secondary','Integration Secondary','http://upstream:9002') RETURNING id",
      [seeded.providerId, secondSecret],
    );
    const secondId = second.rows[0].id;

    // Mirrors PATCH /models/:id: wipe mappings, then reinsert in request order.
    const reassign = async (accountIds: string[]) => {
      await client.query('DELETE FROM model_account_mappings WHERE model_alias_id=$1', [modelId]);
      for (let i = 0; i < accountIds.length; i++) {
        await client.query('INSERT INTO model_account_mappings (model_alias_id,account_id,position) VALUES ($1,$2,$3)', [modelId, accountIds[i], i]);
      }
    };

    await reassign([secondId, seeded.accountId]);

    // Mirrors the GET /models projection. json_agg must explicitly order by
    // position, otherwise the API can return a different order after cleanup.
    const apiProjection = await client.query(
      `SELECT COALESCE(json_agg(mam.account_id ORDER BY mam.position)
        FILTER (WHERE mam.account_id IS NOT NULL),'[]') accounts
       FROM model_aliases ma
       LEFT JOIN model_account_mappings mam ON mam.model_alias_id=ma.id
       WHERE ma.id=$1 GROUP BY ma.id`,
      [modelId],
    );
    assert.deepEqual(apiProjection.rows[0].accounts, [secondId, seeded.accountId], 'GET /models must preserve mapping positions');

    const both = await compileSnapshot(client);
    const compiledModel = both.snapshot.models.find((m: any) => m.alias === 'itest-model');
    assert.ok(compiledModel, 'compiled snapshot is missing itest-model');
    assert.deepEqual(compiledModel.accounts, ['itest-secondary', 'itest-primary'], 'snapshot must honour mapping order');

    // Reassignment is a replace, not an append: dropping an account removes it.
    await reassign([seeded.accountId]);
    const single = await compileSnapshot(client);
    const afterDrop = single.snapshot.models.find((m: any) => m.alias === 'itest-model');
    assert.ok(afterDrop, 'compiled snapshot is missing itest-model after reassignment');
    assert.deepEqual(afterDrop.accounts, ['itest-primary']);
    assert.notEqual(single.checksum, both.checksum, 'checksum must change when routing changes');

    const rows = await client.query('SELECT count(*)::int n FROM model_account_mappings WHERE model_alias_id=$1', [modelId]);
    assert.equal(rows.rows[0].n, 1, 'stale mappings must not survive a reassignment');
  });
});

test.after(async () => { await pool?.end(); });
