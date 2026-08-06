import type { PoolClient } from 'pg';
import { env } from './env';
import { sha256Canonical } from './canonical-json';
import { decryptSecretJson, type EncryptedSecretRecord } from './crypto';

export type CompiledDeclarativeSnapshot = { snapshot: any; checksum: string; schemaVersion: number };
export type RuntimeSnapshot = any;

export async function compileDeclarativeSnapshot(client: PoolClient): Promise<CompiledDeclarativeSnapshot> {
  const accountsR = await client.query(`SELECT a.*, p.adapter FROM accounts a JOIN providers p ON p.id=a.provider_id WHERE a.enabled ORDER BY a.priority, a.name`);
  const modelsR = await client.query('SELECT * FROM model_aliases WHERE enabled ORDER BY alias');
  const mapsR = await client.query(`SELECT mam.*, a.name account_name, ma.alias FROM model_account_mappings mam JOIN accounts a ON a.id=mam.account_id JOIN model_aliases ma ON ma.id=mam.model_alias_id WHERE mam.enabled AND a.enabled ORDER BY mam.tier, mam.position`);
  const routingR = await client.query('SELECT * FROM routing_settings WHERE id=true');
  const keysR = await client.query('SELECT * FROM virtual_keys WHERE enabled ORDER BY name');
  const accounts = accountsR.rows.map((account: any) => ({ id: account.id, name: account.name, adapter: account.adapter, base_url: account.base_url, credential_revision: Number(account.credential_revision ?? 1), enabled: account.enabled, priority: account.priority, weight: account.weight, max_concurrency: account.max_concurrency, cost: Number(account.cost) }));
  const byAlias = new Map<string, string[]>();
  for (const m of mapsR.rows) byAlias.set(m.alias, [...(byAlias.get(m.alias) ?? []), m.account_name]);
  const models = modelsR.rows.map((m: any) => {
    const accountNames = byAlias.get(m.alias) ?? [];
    if (accountNames.length === 0) throw new Error(`model ${m.alias} has no eligible accounts`);
    return { alias: m.alias, upstream_model: m.upstream_model, accounts: accountNames };
  });
  if (models.length === 0) throw new Error('at least one model required');
  const routing = routingR.rows[0] ?? { strategy: 'balanced', sticky_ttl: '1h', max_attempts: 2, resilience: { cooldown: '30s', circuit_failures: 3, circuit_reset: '1m' } };
  const snapshot = { version: 2, metadata: {}, server: { addr: ':8080', read_timeout: '10s', write_timeout: '0s' }, virtual_keys: keysR.rows.map((k: any) => ({ name: k.name, key_hash: k.key_hash, models: k.models, rpm: k.rpm })), models, accounts, routing: { strategy: routing.strategy, sticky_ttl: routing.sticky_ttl, max_attempts: routing.max_attempts }, resilience: routing.resilience };
  if (snapshot.virtual_keys.length === 0) throw new Error('at least one virtual key required');
  return { snapshot, checksum: sha256Canonical(snapshot), schemaVersion: 2 };
}

async function credentialByAccountId(client: PoolClient, accountIds: string[]) {
  const rows = await client.query(`SELECT a.id account_id, sr.* FROM accounts a JOIN secret_records sr ON sr.id=a.secret_record_id WHERE a.id = ANY($1::uuid[])`, [accountIds]);
  return new Map(rows.rows.map((r: any) => [r.account_id, decryptSecretJson<any>(rowToEncrypted(r))]));
}

export async function materializeRuntimeSnapshot(client: PoolClient, declarative: any): Promise<RuntimeSnapshot> {
  const accountIds = (declarative.accounts ?? []).map((a: any) => a.id);
  const secrets = await credentialByAccountId(client, accountIds);
  const accounts = (declarative.accounts ?? []).map((a: any) => {
    const credential = secrets.get(a.id);
    if (!credential) throw new Error(`account ${a.id} missing credential`);
    return { ...a, credential };
  });
  return { ...declarative, version: 2, secret: env.GATEWAY_SHARED_SECRET, accounts };
}

export async function materializeLegacyRuntimeSnapshot(client: PoolClient, declarative: any): Promise<RuntimeSnapshot> {
  const accountIds = (declarative.accounts ?? []).map((a: any) => a.id);
  const secrets = await credentialByAccountId(client, accountIds);
  const accounts = (declarative.accounts ?? []).map((a: any) => {
    const credential = secrets.get(a.id);
    if (!credential?.api_key) throw new Error(`account ${a.id} missing credential`);
    return { name: a.name, base_url: a.base_url, api_key: credential.api_key, enabled: a.enabled, priority: a.priority, weight: a.weight, max_concurrency: a.max_concurrency, cost: a.cost };
  });
  return { version: 1, metadata: declarative.metadata ?? {}, secret: env.GATEWAY_SHARED_SECRET, server: declarative.server, virtual_keys: declarative.virtual_keys, models: declarative.models, accounts, routing: declarative.routing, resilience: declarative.resilience };
}

async function publishedDeclarativeSnapshot(client: PoolClient, version?: string | number) {
  const q = version
    ? await client.query('SELECT version,snapshot FROM config_versions WHERE version=$1 AND status=$2', [version, 'published'])
    : await client.query("SELECT version,snapshot FROM config_versions WHERE status='published' ORDER BY version DESC LIMIT 1");
  const row = q.rows[0];
  if (!row) return null;
  return { version: Number(row.version), declarative: row.snapshot };
}

export async function runtimeSnapshotFromPublishedRow(client: PoolClient, version?: string | number) {
  const row = await publishedDeclarativeSnapshot(client, version);
  if (!row) return null;
  const snapshot = await materializeRuntimeSnapshot(client, row.declarative);
  return { version: row.version, checksum: sha256Canonical(snapshot), snapshot };
}

export async function legacyRuntimeSnapshotFromPublishedRow(client: PoolClient, version?: string | number) {
  const row = await publishedDeclarativeSnapshot(client, version);
  if (!row) return null;
  const snapshot = await materializeLegacyRuntimeSnapshot(client, row.declarative);
  return { version: row.version, checksum: sha256Canonical(snapshot), snapshot };
}

function b64(v: Buffer) { return v.toString('base64'); }
function rowToEncrypted(row: any): EncryptedSecretRecord { return { key_version: row.key_version, data_key_nonce: b64(row.data_key_nonce), data_key_ciphertext: b64(row.data_key_ciphertext), data_key_tag: b64(row.data_key_tag), secret_nonce: b64(row.secret_nonce), secret_ciphertext: b64(row.secret_ciphertext), secret_tag: b64(row.secret_tag) }; }

export const FORBIDDEN_SNAPSHOT_PATTERN = /integration-secret|dev-secret-change-me|api_key|access_token|refresh_token|id_token/i;
