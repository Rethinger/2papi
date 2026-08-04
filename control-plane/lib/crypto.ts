import crypto from 'node:crypto';
import { masterKeyBytes } from './env';

export type EncryptedSecretRecord = {
  key_version: number;
  data_key_nonce: string;
  data_key_ciphertext: string;
  data_key_tag: string;
  secret_nonce: string;
  secret_ciphertext: string;
  secret_tag: string;
};

function seal(key: Buffer, plaintext: Buffer, aad: string) {
  const nonce = crypto.randomBytes(12);
  const cipher = crypto.createCipheriv('aes-256-gcm', key, nonce);
  cipher.setAAD(Buffer.from(aad));
  const ciphertext = Buffer.concat([cipher.update(plaintext), cipher.final()]);
  return { nonce, ciphertext, tag: cipher.getAuthTag() };
}

function open(key: Buffer, part: { nonce: Buffer; ciphertext: Buffer; tag: Buffer }, aad: string) {
  const decipher = crypto.createDecipheriv('aes-256-gcm', key, part.nonce);
  decipher.setAAD(Buffer.from(aad));
  decipher.setAuthTag(part.tag);
  return Buffer.concat([decipher.update(part.ciphertext), decipher.final()]);
}

export function encryptSecretJson(value: unknown, keyVersion = 1, masterKey = masterKeyBytes()): EncryptedSecretRecord {
  const dataKey = crypto.randomBytes(32);
  const secret = seal(dataKey, Buffer.from(JSON.stringify(value)), `secret:v${keyVersion}`);
  const wrapped = seal(masterKey, dataKey, `data-key:v${keyVersion}`);
  return {
    key_version: keyVersion,
    data_key_nonce: wrapped.nonce.toString('base64'),
    data_key_ciphertext: wrapped.ciphertext.toString('base64'),
    data_key_tag: wrapped.tag.toString('base64'),
    secret_nonce: secret.nonce.toString('base64'),
    secret_ciphertext: secret.ciphertext.toString('base64'),
    secret_tag: secret.tag.toString('base64'),
  };
}

export function decryptSecretJson<T = unknown>(record: EncryptedSecretRecord, masterKey = masterKeyBytes()): T {
  const dataKey = open(masterKey, {
    nonce: Buffer.from(record.data_key_nonce, 'base64'),
    ciphertext: Buffer.from(record.data_key_ciphertext, 'base64'),
    tag: Buffer.from(record.data_key_tag, 'base64'),
  }, `data-key:v${record.key_version}`);
  const plaintext = open(dataKey, {
    nonce: Buffer.from(record.secret_nonce, 'base64'),
    ciphertext: Buffer.from(record.secret_ciphertext, 'base64'),
    tag: Buffer.from(record.secret_tag, 'base64'),
  }, `secret:v${record.key_version}`);
  return JSON.parse(plaintext.toString('utf8')) as T;
}

export const secretPresence = (row: { id: string; key_version: number; rotated_at: Date | string }) => ({
  secret_id: row.id,
  secret_present: true,
  key_version: row.key_version,
  rotated_at: row.rotated_at,
});

export function redactSecrets(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(redactSecrets);
  if (!value || typeof value !== 'object') return value;
  return Object.fromEntries(Object.entries(value as Record<string, unknown>).map(([k, v]) => [
    k,
    /api[_-]?key|secret|token|credential|password/i.test(k) ? '[redacted]' : redactSecrets(v),
  ]));
}
