import test from 'node:test';
import assert from 'node:assert/strict';
import { compileSnapshot } from '../lib/control.ts';
import { encryptSecretJson } from '../lib/crypto.ts';

const enc = encryptSecretJson({ api_key: 'upstream-primary' });
const buf = (v: string) => Buffer.from(v, 'base64');

test('snapshot compiler emits Go-compatible v1 shape and detached checksum', async () => {
  const query = async (sql: string) => {
    if (sql.startsWith('SELECT * FROM accounts')) return { rows: [{ id: 'a1', name: 'primary', base_url: 'http://fake-upstream:9001', enabled: true, priority: 1, weight: 2, max_concurrency: 100, cost: '0.15', secret_record_id: 's1' }] };
    if (sql.startsWith('SELECT * FROM secret_records')) return { rows: [{ id: 's1', key_version: enc.key_version, data_key_nonce: buf(enc.data_key_nonce), data_key_ciphertext: buf(enc.data_key_ciphertext), data_key_tag: buf(enc.data_key_tag), secret_nonce: buf(enc.secret_nonce), secret_ciphertext: buf(enc.secret_ciphertext), secret_tag: buf(enc.secret_tag) }] };
    if (sql.startsWith('SELECT * FROM model_aliases')) return { rows: [{ id: 'm1', alias: 'gpt-dev', upstream_model: 'gpt-4o-mini' }] };
    if (sql.startsWith('SELECT mam')) return { rows: [{ alias: 'gpt-dev', account_name: 'primary' }] };
    if (sql.startsWith('SELECT * FROM routing_settings')) return { rows: [{ strategy: 'balanced', sticky_ttl: '1h', max_attempts: 2, resilience: { cooldown: '30s', circuit_failures: 3, circuit_reset: '1m' } }] };
    if (sql.startsWith('SELECT * FROM virtual_keys')) return { rows: [{ name: 'dev', key_hash: 'a'.repeat(64), models: ['gpt-dev'], rpm: 60 }] };
    throw new Error(sql);
  };
  const { snapshot, checksum } = await compileSnapshot({ query } as any);
  assert.equal(snapshot.version, 1);
  assert.equal(snapshot.accounts[0].api_key, 'upstream-primary');
  assert.deepEqual(snapshot.models[0].accounts, ['primary']);
  assert.equal(snapshot.virtual_keys[0].key_hash, 'a'.repeat(64));
  assert.equal('checksum' in snapshot.metadata, false);
  assert.equal('key' in snapshot.virtual_keys[0], false);
  assert.equal(checksum.length, 64);
});
