import test from 'node:test';
import assert from 'node:assert/strict';
import crypto from 'node:crypto';
import { encryptSecretJson, decryptSecretJson, redactSecrets, secretPresence } from '../lib/crypto.ts';

const key = crypto.randomBytes(32);

test('envelope encryption round trip hides plaintext', () => {
  const record = encryptSecretJson({ api_key: 'sk-secret', nested: { token: 'abc' } }, 1, key);
  assert.equal(JSON.stringify(record).includes('sk-secret'), false);
  assert.deepEqual(decryptSecretJson(record, key), { api_key: 'sk-secret', nested: { token: 'abc' } });
});

test('wrong master key fails authentication', () => {
  const record = encryptSecretJson({ api_key: 'sk-secret' }, 1, key);
  assert.throws(() => decryptSecretJson(record, crypto.randomBytes(32)));
});

test('tampering fails authentication', () => {
  const record = encryptSecretJson({ api_key: 'sk-secret' }, 1, key);
  record.secret_ciphertext = Buffer.from('tampered').toString('base64');
  assert.throws(() => decryptSecretJson(record, key));
});

test('redaction and presence metadata do not leak secrets', () => {
  assert.deepEqual(redactSecrets({ api_key: 'x', nested: { token: 'y', ok: true } }), { api_key: '[redacted]', nested: { token: '[redacted]', ok: true } });
  assert.deepEqual(secretPresence({ id: 's1', key_version: 1, rotated_at: 'now' }), { secret_id: 's1', secret_present: true, key_version: 1, rotated_at: 'now' });
});
