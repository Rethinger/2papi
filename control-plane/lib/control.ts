import { z } from 'zod';
import type { PoolClient } from 'pg';
import { decryptSecretJson, encryptSecretJson, type EncryptedSecretRecord } from './crypto';
import { compileDeclarativeSnapshot, materializeLegacyRuntimeSnapshot } from './snapshots';
import { sha256Canonical } from './canonical-json';

export const ProviderSchema = z.object({ slug: z.string().min(1), name: z.string().min(1), adapter: z.string().default('openai-compatible'), base_url: z.string().url(), enabled: z.boolean().default(true), metadata: z.record(z.string(), z.unknown()).default({}) });
export const AccountSchema = z.object({ provider_id: z.string().uuid(), name: z.string().min(1), display_name: z.string().min(1), base_url: z.string().url(), enabled: z.boolean().default(true), priority: z.number().int().default(1), weight: z.number().int().positive().default(1), max_concurrency: z.number().int().positive().default(100), cost: z.number().nonnegative().default(0), credential: z.object({ api_key: z.string().min(1) }).optional(), metadata: z.record(z.string(), z.unknown()).default({}) });
export const ModelSchema = z.object({ alias: z.string().min(1), upstream_model: z.string().min(1), enabled: z.boolean().default(true), accounts: z.array(z.string().uuid()).min(1) });
export const RoutingSchema = z.object({ strategy: z.enum(['balanced','priority','weighted']).default('balanced'), sticky_ttl: z.string().default('1h'), max_attempts: z.number().int().positive().default(2), resilience: z.object({ cooldown: z.string().default('30s'), circuit_failures: z.number().int().positive().default(3), circuit_reset: z.string().default('1m') }).default({ cooldown: '30s', circuit_failures: 3, circuit_reset: '1m' }) });
export const VirtualKeySchema = z.object({ name: z.string().min(1), plaintext_key: z.string().min(8).optional(), enabled: z.boolean().default(true), models: z.array(z.string()).default([]), rpm: z.number().int().positive().default(60) });

function b64ToBuf(v: string) { return Buffer.from(v, 'base64'); }
function bufToB64(v: Buffer) { return v.toString('base64'); }
export function rowToEncrypted(row: any): EncryptedSecretRecord { return { key_version: row.key_version, data_key_nonce: bufToB64(row.data_key_nonce), data_key_ciphertext: bufToB64(row.data_key_ciphertext), data_key_tag: bufToB64(row.data_key_tag), secret_nonce: bufToB64(row.secret_nonce), secret_ciphertext: bufToB64(row.secret_ciphertext), secret_tag: bufToB64(row.secret_tag) }; }

export async function insertSecret(client: PoolClient, purpose: string, credential: unknown) {
  const e = encryptSecretJson(credential);
  const r = await client.query(`INSERT INTO secret_records (purpose,key_version,data_key_nonce,data_key_ciphertext,data_key_tag,secret_nonce,secret_ciphertext,secret_tag) VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`, [purpose, e.key_version, b64ToBuf(e.data_key_nonce), b64ToBuf(e.data_key_ciphertext), b64ToBuf(e.data_key_tag), b64ToBuf(e.secret_nonce), b64ToBuf(e.secret_ciphertext), b64ToBuf(e.secret_tag)]);
  return r.rows[0].id as string;
}

export async function audit(client: PoolClient, action: string, resourceType: string, resourceId?: string, payload: unknown = {}) {
  await client.query('INSERT INTO audit_events (action, resource_type, resource_id, payload) VALUES ($1,$2,$3,$4)', [action, resourceType, resourceId ?? null, JSON.stringify(payload)]);
}

export async function compileSnapshot(client: PoolClient) {
  const compiled = await compileDeclarativeSnapshot(client);
  const runtime = await materializeLegacyRuntimeSnapshot(client, compiled.snapshot);
  return { snapshot: runtime, checksum: sha256Canonical(runtime) };
}

export async function storeDraft(client: PoolClient) {
  const compiled = await compileDeclarativeSnapshot(client);
  const r = await client.query('INSERT INTO config_versions (status, checksum, config_checksum, schema_version, snapshot) VALUES ($1,$2,$2,$3,$4) RETURNING version, checksum, status, created_at', ['draft', compiled.checksum, compiled.schemaVersion, JSON.stringify(compiled.snapshot)]);
  return r.rows[0];
}

export async function publishLatest(client: PoolClient) {
  const draft = await client.query("SELECT * FROM config_versions WHERE status='draft' ORDER BY version DESC LIMIT 1");
  const row = draft.rows[0] ?? (await storeDraft(client));
  const version = row.version;
  await client.query("UPDATE config_versions SET status='published', published_at=now() WHERE version=$1", [version]);
  await audit(client, 'publish', 'config_version', String(version), { checksum: row.checksum });
  return { version: Number(version), checksum: row.checksum };
}
