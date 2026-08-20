import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import { Pool } from 'pg';
import { insertSecret, storeDraft } from '../lib/control.ts';
import { sanitizeHistoricalConfigVersions } from '../lib/snapshot-migration.ts';

const url = process.env.TEST_DATABASE_URL;
const options = { skip: url ? false : 'TEST_DATABASE_URL is not set' };
const schema = `snapshot_security_${process.pid}`;
const pool = url ? new Pool({ connectionString: `${url}?options=-c%20search_path%3D${schema},public`, max: 4 }) : null;

async function sql(name: string) { return fs.readFile(path.join(process.cwd(), 'migrations', name), 'utf8'); }
async function tx<T>(fn: (c: any) => Promise<T>) { const c = await pool!.connect(); try { await c.query('BEGIN'); const v = await fn(c); await c.query('COMMIT'); return v; } catch (e) { await c.query('ROLLBACK'); throw e; } finally { c.release(); } }

async function seedBase(c: any) {
  const provider = await c.query("INSERT INTO providers (slug,name,adapter,base_url) VALUES ('sec','Security','openai-compatible','http://upstream:9001') RETURNING id");
  const secret = await insertSecret(c, 'account_credential', { api_key: 'integration-secret' });
  const account = await c.query("INSERT INTO accounts (provider_id,secret_record_id,name,display_name,base_url) VALUES ($1,$2,'sec-primary','Security Primary','http://upstream:9001') RETURNING id", [provider.rows[0].id, secret]);
  const model = await c.query("INSERT INTO model_aliases (alias,upstream_model) VALUES ('sec-model','gpt-4o-mini') RETURNING id");
  await c.query('INSERT INTO model_account_mappings (model_alias_id,account_id,position) VALUES ($1,$2,0)', [model.rows[0].id, account.rows[0].id]);
  await c.query("INSERT INTO routing_settings (id,strategy,sticky_ttl,max_attempts) VALUES (true,'balanced','1h',2) ON CONFLICT (id) DO NOTHING");
  await c.query("INSERT INTO virtual_keys (name,key_hash,key_prefix,models,rpm) VALUES ('sec-key',$1,'sk-sec',ARRAY['sec-model'],60)", ['c'.repeat(64)]);
}

test.before(async () => {
  if (!url) return;
  await pool!.query(`DROP SCHEMA IF EXISTS ${schema} CASCADE; CREATE SCHEMA ${schema};`);
  await pool!.query((await sql('001_schema.sql')).replace('CREATE EXTENSION IF NOT EXISTS pgcrypto;', ''));
});

test('snapshot security migrations reconstruct history and keep snapshots credential-free', options, async () => {
  await tx(async c => {
    await seedBase(c);
    const legacy = { version: 1, metadata: { compiled_at: 'legacy' }, secret: 'dev-secret-change-me', server: { addr: ':8080' }, virtual_keys: [{ name: 'sec-key', key_hash: 'c'.repeat(64), models: ['sec-model'], rpm: 60 }], models: [{ alias: 'sec-model', upstream_model: 'gpt-4o-mini', accounts: ['sec-primary'] }], accounts: [{ name: 'sec-primary', base_url: 'http://upstream:9001', api_key: 'integration-secret', enabled: true, priority: 1, weight: 1, max_concurrency: 100, cost: 0 }], routing: { strategy: 'balanced', sticky_ttl: '1h', max_attempts: 2 }, resilience: {} };
    const published = await c.query("INSERT INTO config_versions (status,checksum,snapshot,published_at) VALUES ('published','legacy',$1,now()) RETURNING version", [JSON.stringify(legacy)]);
    const rolled = await c.query("INSERT INTO config_versions (status,checksum,snapshot,source_version) VALUES ('rolled_back','legacy2',$1,$2) RETURNING version", [JSON.stringify(legacy), published.rows[0].version]);
    await c.query("INSERT INTO config_versions (status,checksum,snapshot,errors,source_version) VALUES ('draft','bad',$1,$2,$3)", [JSON.stringify({ accounts: [{ name: 'missing', api_key: 'integration-secret' }] }), JSON.stringify([{ code: 'prior' }]), rolled.rows[0].version]);
    await c.query("INSERT INTO config_versions (status,checksum,snapshot,errors,source_version) VALUES ('draft','unsafe-nested',$1,$2,$3)", [JSON.stringify({ ...legacy, metadata: { nested: { access_token: 'must-not-survive' } } }), JSON.stringify([{ code: 'prior-unsafe' }]), rolled.rows[0].version]);
    await c.query(await sql('002_snapshot_security.sql'));
    await c.query(await sql('003_codex_provider.sql'));
    await c.query(await sql('006_provider_model_pools.sql'));
    await c.query(await sql('010_teams.sql'));
    await sanitizeHistoricalConfigVersions(c);

    const rows = await c.query('SELECT version,status,snapshot,checksum,config_checksum,errors,source_version,published_at FROM config_versions ORDER BY version');
    assert.equal(rows.rows[0].status, 'published');
    assert.ok(rows.rows[0].published_at);
    assert.equal(rows.rows[1].status, 'rolled_back');
    assert.equal(Number(rows.rows[1].source_version), Number(published.rows[0].version));
    assert.equal(rows.rows[2].status, 'invalid');
    assert.deepEqual(rows.rows[2].errors.at(-1), { code: 'snapshot_reconstruction_failed', migration: '002_snapshot_security' });
    assert.equal(rows.rows[3].status, 'invalid');
    assert.deepEqual(rows.rows[3].errors, [{ code: 'prior-unsafe' }, { code: 'snapshot_reconstruction_failed', migration: '002_snapshot_security' }]);
    for (const row of rows.rows.slice(0, 2)) {
      assert.equal(row.checksum, row.config_checksum);
      const body = JSON.stringify(row.snapshot);
      assert.ok(!/integration-secret|api_key|access_token|refresh_token|id_token|"secret"/i.test(body), body);
      assert.equal(row.snapshot.accounts[0].credential_revision, 1);
      assert.equal(row.snapshot.accounts[0].adapter, 'openai-compatible');
    }
    const draft = await storeDraft(c);
    const stored = await c.query('SELECT snapshot::text body FROM config_versions WHERE version=$1', [draft.version]);
    assert.ok(!stored.rows[0].body.includes('integration-secret'));
    assert.ok(!stored.rows[0].body.includes('api_key'));
    assert.ok(!stored.rows[0].body.includes('"secret"'));
    const declarative = JSON.parse(stored.rows[0].body);
    assert.equal(declarative.accounts[0].credential_revision, 1);
    assert.equal(declarative.accounts[0].adapter, 'openai-compatible');

    for (const table of ['gateway_instances','discovered_models','account_provider_state','provider_operations']) {
      const regclass = (await c.query('SELECT to_regclass($1) ok', [`${schema}.${table}`])).rows[0].ok;
      assert.ok(regclass === `${schema}.${table}` || regclass === table, regclass);
    }
    const idx = await c.query("SELECT indexdef FROM pg_indexes WHERE indexname='provider_operations_one_active_reset'");
    assert.match(idx.rows[0].indexdef, /WHERE .*quota_reset.*pending.*unknown/i);
    const marker = (await c.query("SELECT result FROM snapshot_migration_state WHERE migration='002_snapshot_security'" )).rows[0].result;
    assert.deepEqual(marker, { reconstructed: 2, invalid: 2 });
  });
});

test('historical reconstruction rejects changed base url instead of matching by name only', options, async () => {
  await tx(async c => {
    await c.query('TRUNCATE snapshot_migration_state,config_versions,model_account_mappings,model_aliases,accounts,providers,secret_records,virtual_keys CASCADE');
    await seedBase(c);
    const changedUrl = { version: 1, metadata: {}, server: { addr: ':8080' }, virtual_keys: [{ name: 'sec-key', key_hash: 'c'.repeat(64), models: ['sec-model'], rpm: 60 }], models: [{ alias: 'sec-model', upstream_model: 'gpt-4o-mini', accounts: ['sec-primary'] }], accounts: [{ name: 'sec-primary', base_url: 'http://old-upstream:9001', api_key: 'integration-secret', enabled: true, priority: 1, weight: 1, max_concurrency: 100, cost: 0 }], routing: { strategy: 'balanced', sticky_ttl: '1h', max_attempts: 2 }, resilience: {} };
    const missingUrl = { ...changedUrl, accounts: [{ ...changedUrl.accounts[0], base_url: undefined }] };
    await c.query("INSERT INTO config_versions (status,checksum,snapshot) VALUES ('draft','changed-url',$1),('draft','missing-url',$2)", [JSON.stringify(changedUrl), JSON.stringify(missingUrl)]);
    await c.query(await sql('002_snapshot_security.sql'));
    await sanitizeHistoricalConfigVersions(c);
    const rows = (await c.query('SELECT status,errors FROM config_versions ORDER BY version')).rows;
    assert.equal(rows.length, 2);
    for (const row of rows) {
      assert.equal(row.status, 'invalid');
      assert.deepEqual(row.errors.at(-1), { code: 'snapshot_reconstruction_failed', migration: '002_snapshot_security' });
    }
  });
});

test('historical reconstruction rejects ambiguous legacy name matches', options, async () => {
  await tx(async c => {
    await c.query('TRUNCATE snapshot_migration_state,config_versions,model_account_mappings,model_aliases,accounts,providers,secret_records,virtual_keys CASCADE');
    await c.query('ALTER TABLE accounts DROP CONSTRAINT IF EXISTS accounts_name_key');
    await seedBase(c);
    const provider = await c.query("INSERT INTO providers (slug,name,adapter,base_url) VALUES ('sec2','Security 2','openai-compatible','http://upstream:9001') RETURNING id");
    const secret = await insertSecret(c, 'account_credential', { api_key: 'other-secret' });
    await c.query("INSERT INTO accounts (provider_id,secret_record_id,name,display_name,base_url) VALUES ($1,$2,'sec-primary','Duplicate','http://upstream:9001')", [provider.rows[0].id, secret]);
    const legacy = { version: 1, metadata: {}, server: { addr: ':8080' }, virtual_keys: [{ name: 'sec-key', key_hash: 'c'.repeat(64), models: ['sec-model'], rpm: 60 }], models: [{ alias: 'sec-model', upstream_model: 'gpt-4o-mini', accounts: ['sec-primary'] }], accounts: [{ name: 'sec-primary', base_url: 'http://upstream:9001', api_key: 'integration-secret', enabled: true, priority: 1, weight: 1, max_concurrency: 100, cost: 0 }], routing: { strategy: 'balanced', sticky_ttl: '1h', max_attempts: 2 }, resilience: {} };
    await c.query("INSERT INTO config_versions (status,checksum,snapshot) VALUES ('draft','legacy',$1)", [JSON.stringify(legacy)]);
    await c.query(await sql('002_snapshot_security.sql'));
    await sanitizeHistoricalConfigVersions(c);
    const row = (await c.query('SELECT status,errors FROM config_versions')).rows[0];
    assert.equal(row.status, 'invalid');
    assert.deepEqual(row.errors.at(-1), { code: 'snapshot_reconstruction_failed', migration: '002_snapshot_security' });
  });
});

test.after(async () => { await pool?.end(); });
