import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import crypto from 'node:crypto';
import { Pool } from 'pg';
import { canonicalJson, sha256Canonical } from '../lib/canonical-json.ts';
import { assertSchemaV2Publishable, insertSecret, publishLatest, storeDraft } from '../lib/control.ts';
import { materializeLegacyRuntimeSnapshot, materializeRuntimeSnapshot } from '../lib/snapshots.ts';

const url = process.env.TEST_DATABASE_URL;
const options = { skip: url ? false : 'TEST_DATABASE_URL is not set' };
const schema = `snapshot_envelope_${process.pid}`;
const pool = url ? new Pool({ connectionString: `${url}?options=-c%20search_path%3D${schema},public`, max: 4 }) : null;
async function sql(name: string) { return fs.readFile(path.join(process.cwd(), 'migrations', name), 'utf8'); }
async function tx<T>(fn: (c: any) => Promise<T>) { const c = await pool!.connect(); try { await c.query('BEGIN'); const v = await fn(c); await c.query('COMMIT'); return v; } catch (e) { await c.query('ROLLBACK'); throw e; } finally { c.release(); } }
async function migrate() { await pool!.query(`DROP SCHEMA IF EXISTS ${schema} CASCADE; CREATE SCHEMA ${schema};`); await pool!.query((await sql('001_schema.sql')).replace('CREATE EXTENSION IF NOT EXISTS pgcrypto;', '')); await pool!.query(await sql('002_snapshot_security.sql')); await pool!.query(await sql('003_codex_provider.sql')); }
async function seed(c: any, adapter = 'openai-compatible') {
  const provider = await c.query('INSERT INTO providers (slug,name,adapter,base_url) VALUES ($1,$2,$3,$4) RETURNING id', [adapter, adapter, adapter, 'http://upstream:9001']);
  const secret = await insertSecret(c, 'account_credential', { api_key: `${adapter}-current-key` });
  const account = await c.query('INSERT INTO accounts (provider_id,secret_record_id,name,display_name,base_url) VALUES ($1,$2,$3,$4,$5) RETURNING id', [provider.rows[0].id, secret, `${adapter}-account`, adapter, 'http://upstream:9001']);
  const model = await c.query('INSERT INTO model_aliases (alias,upstream_model) VALUES ($1,$2) RETURNING id', [`${adapter}-model`, 'gpt-4o-mini']);
  await c.query('INSERT INTO model_account_mappings (model_alias_id,account_id,position) VALUES ($1,$2,0)', [model.rows[0].id, account.rows[0].id]);
  await c.query("INSERT INTO routing_settings (id,strategy,sticky_ttl,max_attempts) VALUES (true,'balanced','1h',2) ON CONFLICT (id) DO NOTHING");
  await c.query('INSERT INTO virtual_keys (name,key_hash,key_prefix,models,rpm) VALUES ($1,$2,$3,$4,60)', [`${adapter}-key`, 'd'.repeat(64), 'sk-env', [`${adapter}-model`]]);
}
const checksum = (v: unknown) => crypto.createHash('sha256').update(canonicalJson(v)).digest('hex');
async function envelope(c: any, v2: boolean) {
  const row = (await c.query("SELECT * FROM config_versions WHERE status='published' ORDER BY version DESC LIMIT 1")).rows[0];
  const snapshot = v2 ? await materializeRuntimeSnapshot(c, row.snapshot) : await materializeLegacyRuntimeSnapshot(c, row.snapshot);
  const runtime_checksum = checksum(snapshot);
  return v2 ? { config_version: Number(row.version), schema_version: Number(row.schema_version), config_checksum: row.config_checksum, credential_digest: sha256Canonical((snapshot.accounts ?? []).map((a: any) => ({ id: a.id ?? a.name, credential: a.credential ?? a.api_key ?? null }))), runtime_checksum, snapshot } : { version: Number(row.version), checksum: runtime_checksum, snapshot };
}

test.before(async () => { if (url) await migrate(); });

test('snapshot endpoint serves legacy and v2 envelopes with exact runtime checksum', options, async () => {
  await tx(async c => { await seed(c); const draft = await storeDraft(c); await c.query("UPDATE config_versions SET status='published', published_at=now() WHERE version=$1", [draft.version]); const legacy = await envelope(c, false); assert.equal(legacy.snapshot.version, 1); assert.equal(legacy.checksum, checksum(legacy.snapshot)); const v2 = await envelope(c, true); assert.equal(v2.schema_version, 2); assert.equal(v2.snapshot.version, 2); assert.equal(v2.runtime_checksum, checksum(v2.snapshot)); });
});

test('v1-only active gateways block v2 publish until adopted v2 ack and stale gateways do not count', options, async () => {
  await tx(async c => { await c.query('TRUNCATE gateway_config_acks,gateway_instances,config_versions,model_account_mappings,model_aliases,accounts,providers,secret_records,virtual_keys CASCADE'); await seed(c, 'codex'); const draft = await storeDraft(c); await c.query("INSERT INTO gateway_instances (gateway_id,supported_schemas,envelope_version,last_seen_at) VALUES ('gw-v1',ARRAY[1],1,now())"); await assert.rejects(publishLatest(c), (e: any) => e.status === 426); assert.equal((await c.query("SELECT status FROM config_versions WHERE status='draft'")).rowCount, 1); await c.query("UPDATE gateway_instances SET supported_schemas=ARRAY[1,2], envelope_version=2 WHERE gateway_id='gw-v1'"); await c.query("INSERT INTO gateway_config_acks (gateway_id,version,checksum,status,schema_version,config_checksum,credential_digest,runtime_checksum,envelope_version) VALUES ('gw-v1',$1,'c','adopted',2,'c','d','r',2)", [draft.version]); await assert.doesNotReject(assertSchemaV2Publishable(c)); await c.query("UPDATE gateway_instances SET last_seen_at=now() - interval '1 hour'"); await assert.rejects(assertSchemaV2Publishable(c), (e: any) => e.status === 409); });
});

test('schema v1 rollback bypasses v2 gate and materializes current credentials', options, async () => {
  await tx(async c => { await c.query('TRUNCATE gateway_config_acks,gateway_instances,config_versions,model_account_mappings,model_aliases,accounts,providers,secret_records,virtual_keys CASCADE'); await seed(c); const acct = (await c.query("SELECT id FROM accounts WHERE name='openai-compatible-account'")).rows[0].id; const v1 = { version: 1, metadata: {}, server: { addr: ':8080' }, virtual_keys: [{ name: 'openai-compatible-key', key_hash: 'd'.repeat(64), models: ['openai-compatible-model'], rpm: 60 }], models: [{ alias: 'openai-compatible-model', upstream_model: 'gpt-4o-mini', accounts: ['openai-compatible-account'] }], accounts: [{ id: acct, name: 'openai-compatible-account', base_url: 'http://upstream:9001', enabled: true, priority: 1, weight: 1, max_concurrency: 100, cost: 0 }], routing: { strategy: 'balanced', sticky_ttl: '1h', max_attempts: 2 }, resilience: {} }; const old = await c.query("INSERT INTO config_versions (status,checksum,config_checksum,schema_version,snapshot,published_at) VALUES ('draft','old','old',1,$1,now()) RETURNING version", [JSON.stringify(v1)]); await publishLatest(c); const env = await envelope(c, false); assert.equal(env.snapshot.accounts[0].api_key, 'openai-compatible-current-key'); });
});

test.after(async () => { await pool?.end(); });
