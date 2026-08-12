import test from 'node:test';
import assert from 'node:assert/strict';
import { compileSnapshot } from '../lib/control.ts';
import { encryptSecretJson } from '../lib/crypto.ts';
import { sha256Canonical } from '../lib/canonical-json.ts';
import { materializeRuntimeSnapshot, materializeLegacyRuntimeSnapshot, runtimeSnapshotFromPublishedRow, legacyRuntimeSnapshotFromPublishedRow } from '../lib/snapshots.ts';

const enc = encryptSecretJson({ api_key: 'upstream-primary' });
const buf = (v: string) => Buffer.from(v, 'base64');
const accountId = '00000000-0000-0000-0000-000000000001';
const declarative = {
  version: 2,
  metadata: {},
  server: { addr: ':8080', read_timeout: '10s', write_timeout: '0s' },
  virtual_keys: [{ name: 'dev', key_hash: 'a'.repeat(64), models: ['gpt-dev'], rpm: 60 }],
  models: [{ alias: 'gpt-dev', upstream_model: 'gpt-4o-mini', accounts: ['primary'] }],
  accounts: [{ id: accountId, name: 'primary', adapter: 'openai-compatible', base_url: 'http://fake-upstream:9001', enabled: true, priority: 1, weight: 2, max_concurrency: 100, cost: 0.15, credential_revision: 1 }],
  routing: { strategy: 'balanced', sticky_ttl: '1h', max_attempts: 2 },
  resilience: { cooldown: '30s', circuit_failures: 3, circuit_reset: '1m' },
};

function secretRows() {
  return [{ id: accountId, credential_revision: 1, key_version: enc.key_version, data_key_nonce: buf(enc.data_key_nonce), data_key_ciphertext: buf(enc.data_key_ciphertext), data_key_tag: buf(enc.data_key_tag), secret_nonce: buf(enc.secret_nonce), secret_ciphertext: buf(enc.secret_ciphertext), secret_tag: buf(enc.secret_tag) }];
}

function mockClient(extra?: { noSecret?: boolean; published?: any }) {
  const queries: Array<{ sql: string; params?: unknown[] }> = [];
  const query = async (sql: string, params?: unknown[]) => {
    queries.push({ sql, params });
    if (sql.startsWith('SELECT a.*, p.adapter FROM accounts')) return { rows: declarative.accounts };
    if (sql.startsWith('SELECT a.id account_id, a.credential_revision, sr.* FROM accounts')) return { rows: extra?.noSecret ? [] : secretRows().map(row => ({ ...row, account_id: accountId })) };
    if (sql.startsWith('SELECT * FROM model_aliases')) return { rows: [{ id: 'm1', alias: 'gpt-dev', upstream_model: 'gpt-4o-mini', provider_id: null, routing_strategy: 'manual' }] };
    if (sql.startsWith('SELECT mam')) return { rows: [{ alias: 'gpt-dev', account_name: 'primary' }] };
    if (sql.includes('FROM model_aliases ma') && sql.includes('JOIN discovered_models dm')) return { rows: [] };
    if (sql.startsWith('SELECT * FROM routing_settings')) return { rows: [{ strategy: 'balanced', sticky_ttl: '1h', max_attempts: 2, resilience: declarative.resilience }] };
    if (sql.startsWith('SELECT * FROM virtual_keys')) return { rows: declarative.virtual_keys };
    if (sql.startsWith('SELECT version,snapshot FROM config_versions')) return { rows: extra?.published ? [extra.published] : [] };
    throw new Error(sql);
  };
  return { client: { query } as any, queries };
}

test('snapshot compiler emits Go-compatible v1 runtime shape and checksum over exact payload', async () => {
  const { client } = mockClient();
  const { snapshot, checksum } = await compileSnapshot(client);
  assert.equal(snapshot.version, 1);
  assert.equal(snapshot.secret, 'dev-secret-change-me');
  assert.equal(snapshot.accounts[0].api_key, 'upstream-primary');
  assert.equal('credential' in snapshot.accounts[0], false);
  assert.equal('id' in snapshot.accounts[0], false);
  assert.deepEqual(snapshot.models[0].accounts, ['primary']);
  assert.equal(snapshot.virtual_keys[0].key_hash, 'a'.repeat(64));
  assert.equal('key' in snapshot.virtual_keys[0], false);
  assert.equal(checksum, sha256Canonical(snapshot));
});

test('runtime materialization fails closed when account secret is missing', async () => {
  const { client } = mockClient({ noSecret: true });
  await assert.rejects(() => materializeRuntimeSnapshot(client, declarative), /missing credential/);
});

test('v2 runtime materialization keeps structured credentials in memory only', async () => {
  const { client } = mockClient();
  const runtime = await materializeRuntimeSnapshot(client, declarative);
  assert.equal(runtime.version, 2);
  assert.equal(runtime.secret, 'dev-secret-change-me');
  assert.equal(runtime.accounts[0].credential.api_key, 'upstream-primary');
  assert.equal(runtime.accounts[0].credential.kind, 'api_key');
  assert.equal(runtime.accounts[0].credential.revision, 1);
  assert.equal(runtime.accounts[0].id, accountId);
  assert.equal(runtime.accounts[0].credential_revision, 1);
});

test('legacy runtime materialization emits transitional v1 account api_key shape', async () => {
  const { client } = mockClient();
  const runtime = await materializeLegacyRuntimeSnapshot(client, declarative);
  assert.equal(runtime.version, 1);
  assert.equal(runtime.accounts[0].api_key, 'upstream-primary');
  assert.equal('credential' in runtime.accounts[0], false);
  assert.equal('id' in runtime.accounts[0], false);
});

test('internal snapshot helper loads only published declarative rows and returns runtime checksum without persisting', async () => {
  const { client, queries } = mockClient({ published: { version: 12, checksum: 'declarative', snapshot: declarative } });
  const envelope = await runtimeSnapshotFromPublishedRow(client, 12);
  assert.equal(envelope?.version, 12);
  assert.equal(envelope?.snapshot.version, 2);
  assert.deepEqual(envelope?.snapshot.accounts[0].credential, { api_key: 'upstream-primary', kind: 'api_key', revision: 1 });
  assert.equal(envelope?.snapshot.secret, 'dev-secret-change-me');
  assert.equal(envelope?.checksum, sha256Canonical(envelope?.snapshot));
  assert.ok(queries.some(q => q.sql.includes('status=$2') && (q.params as unknown[])[1] === 'published'));
  assert.equal(queries.some(q => /^\s*(INSERT|UPDATE)/i.test(q.sql)), false);
});

test('legacy internal snapshot helper remains explicit transitional v1', async () => {
  const { client } = mockClient({ published: { version: 13, checksum: 'declarative', snapshot: declarative } });
  const envelope = await legacyRuntimeSnapshotFromPublishedRow(client, 13);
  assert.equal(envelope?.version, 13);
  assert.equal(envelope?.snapshot.version, 1);
  assert.equal(envelope?.snapshot.accounts[0].api_key, 'upstream-primary');
  assert.equal('credential' in envelope?.snapshot.accounts[0], false);
  assert.equal(envelope?.checksum, sha256Canonical(envelope?.snapshot));
});
