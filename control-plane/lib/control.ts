import crypto from 'node:crypto';
import { z } from 'zod';
import type { PoolClient } from 'pg';
import { decryptSecretJson, encryptSecretJson, type EncryptedSecretRecord } from './crypto';
import { env } from './env';

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
  const [accountsR, secretsR, modelsR, mapsR, routingR, keysR] = await Promise.all([
    client.query('SELECT * FROM accounts WHERE enabled ORDER BY priority, name'),
    client.query('SELECT * FROM secret_records'),
    client.query('SELECT * FROM model_aliases WHERE enabled ORDER BY alias'),
    client.query('SELECT mam.*, a.name account_name, ma.alias FROM model_account_mappings mam JOIN accounts a ON a.id=mam.account_id JOIN model_aliases ma ON ma.id=mam.model_alias_id WHERE mam.enabled AND a.enabled ORDER BY mam.tier, mam.position'),
    client.query('SELECT * FROM routing_settings WHERE id=true'),
    client.query('SELECT * FROM virtual_keys WHERE enabled ORDER BY name'),
  ]);
  const secrets = new Map(secretsR.rows.map((r: any) => [r.id, r]));
  const accounts = accountsR.rows.map((a: any) => {
    const sr = secrets.get(a.secret_record_id);
    if (!sr) throw new Error(`account ${a.name} missing credential`);
    const credential = decryptSecretJson<{ api_key: string }>(rowToEncrypted(sr));
    return { name: a.name, base_url: a.base_url, api_key: credential.api_key, enabled: a.enabled, priority: a.priority, weight: a.weight, max_concurrency: a.max_concurrency, cost: Number(a.cost) };
  });
  const byAlias = new Map<string, string[]>();
  for (const m of mapsR.rows) byAlias.set(m.alias, [...(byAlias.get(m.alias) ?? []), m.account_name]);
  const models = modelsR.rows.map((m: any) => {
    const accountNames = byAlias.get(m.alias) ?? [];
    if (accountNames.length === 0) throw new Error(`model ${m.alias} has no eligible accounts`);
    return { alias: m.alias, upstream_model: m.upstream_model, accounts: accountNames };
  });
  if (models.length === 0) throw new Error('at least one model required');
  const routing = routingR.rows[0] ?? { strategy: 'balanced', sticky_ttl: '1h', max_attempts: 2, resilience: { cooldown: '30s', circuit_failures: 3, circuit_reset: '1m' } };
  const snapshot = { version: 1, metadata: { compiled_at: new Date().toISOString() }, secret: env.GATEWAY_SHARED_SECRET, server: { addr: ':8080', read_timeout: '10s', write_timeout: '0s' }, virtual_keys: keysR.rows.map((k: any) => ({ name: k.name, key_hash: k.key_hash, models: k.models, rpm: k.rpm })), models, accounts, routing: { strategy: routing.strategy, sticky_ttl: routing.sticky_ttl, max_attempts: routing.max_attempts }, resilience: routing.resilience };
  if (snapshot.virtual_keys.length === 0) throw new Error('at least one virtual key required');
  const checksum = crypto.createHash('sha256').update(JSON.stringify(snapshot)).digest('hex');
  return { snapshot: { ...snapshot, metadata: { ...snapshot.metadata, checksum } }, checksum };
}

export async function storeDraft(client: PoolClient) {
  const compiled = await compileSnapshot(client);
  const r = await client.query('INSERT INTO config_versions (status, checksum, snapshot) VALUES ($1,$2,$3) RETURNING version, checksum, status, created_at', ['draft', compiled.checksum, JSON.stringify(compiled.snapshot)]);
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
