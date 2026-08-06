import type { PoolClient } from 'pg';
import { sha256Canonical } from './canonical-json';
import { FORBIDDEN_SNAPSHOT_PATTERN } from './snapshots';

const MIGRATION = '002_snapshot_security';

export async function sanitizeHistoricalConfigVersions(client: PoolClient) {
  await client.query('SELECT pg_advisory_xact_lock(hashtext($1))', [MIGRATION]);
  const done = await client.query('SELECT 1 FROM snapshot_migration_state WHERE migration=$1', [MIGRATION]);
  if (done.rowCount) return { skipped: true };
  const rows = await client.query('SELECT * FROM config_versions ORDER BY version FOR UPDATE');
  let reconstructed = 0, invalid = 0;
  for (const row of rows.rows) {
    const result = await reconstruct(client, row.snapshot);
    if (result.ok && !FORBIDDEN_SNAPSHOT_PATTERN.test(JSON.stringify(result.snapshot))) {
      const checksum = sha256Canonical(result.snapshot);
      await client.query('UPDATE config_versions SET snapshot=$2, checksum=$3, config_checksum=$3, schema_version=2 WHERE version=$1', [row.version, JSON.stringify(result.snapshot), checksum]);
      reconstructed++;
    } else {
      const errors = Array.isArray(row.errors) ? row.errors : [];
      await client.query(`UPDATE config_versions SET status='invalid', errors=$2 WHERE version=$1`, [row.version, JSON.stringify([...errors, { code: 'snapshot_reconstruction_failed', migration: MIGRATION }])]);
      invalid++;
    }
  }
  await client.query('INSERT INTO snapshot_migration_state (migration,result) VALUES ($1,$2)', [MIGRATION, JSON.stringify({ reconstructed, invalid })]);
  return { reconstructed, invalid };
}

async function reconstruct(client: PoolClient, legacy: any): Promise<{ ok: true; snapshot: any } | { ok: false }> {
  try {
    if (!legacy || !Array.isArray(legacy.accounts) || !Array.isArray(legacy.models) || !Array.isArray(legacy.virtual_keys)) return { ok: false };
    if (containsUnexpectedCredentialMaterial(legacy)) return { ok: false };
    const accounts = [];
    for (const a of legacy.accounts) {
      if (!a?.name) return { ok: false };
      const r = await client.query(`SELECT a.*, p.adapter, p.base_url provider_base_url FROM accounts a JOIN providers p ON p.id=a.provider_id WHERE a.name=$1`, [a.name]);
      const matches = r.rows.filter((row: any) => legacyAccountMatches(a, row));
      if (matches.length !== 1) return { ok: false };
      const row = matches[0];
      accounts.push({ id: row.id, name: row.name, adapter: row.adapter ?? 'openai-compatible', base_url: row.base_url, credential_revision: Number(row.credential_revision ?? 1), enabled: row.enabled, priority: row.priority, weight: row.weight, max_concurrency: row.max_concurrency, cost: Number(row.cost) });
    }
    const server = pickServer(legacy.server);
    const virtualKeys = legacy.virtual_keys.map(pickVirtualKey);
    const models = legacy.models.map(pickModel);
    const routing = pickRouting(legacy.routing);
    const resilience = pickResilience(legacy.resilience);
    if (!server || virtualKeys.some((value: any) => !value) || models.some((value: any) => !value) || !routing || !resilience) return { ok: false };
    return { ok: true, snapshot: { version: 2, metadata: {}, server, virtual_keys: virtualKeys, models, accounts, routing, resilience } };
  } catch { return { ok: false }; }
}

function pickServer(value: any) {
  if (!isRecord(value)) return null;
  if (!optionalString(value.addr) || !optionalString(value.read_timeout) || !optionalString(value.write_timeout)) return null;
  return { addr: value.addr ?? ':8080', read_timeout: value.read_timeout ?? '10s', write_timeout: value.write_timeout ?? '0s' };
}

function pickVirtualKey(value: any) {
  if (!isRecord(value) || !requiredString(value.name) || !requiredString(value.key_hash) || !stringArray(value.models) || !positiveInteger(value.rpm)) return null;
  return { name: value.name, key_hash: value.key_hash, models: [...value.models], rpm: value.rpm };
}

function pickModel(value: any) {
  if (!isRecord(value) || !requiredString(value.alias) || !requiredString(value.upstream_model) || !stringArray(value.accounts) || value.accounts.length === 0) return null;
  return { alias: value.alias, upstream_model: value.upstream_model, accounts: [...value.accounts] };
}

function pickRouting(value: any) {
  if (!isRecord(value) || !optionalString(value.strategy) || !optionalString(value.sticky_ttl) || !positiveInteger(value.max_attempts)) return null;
  return { strategy: value.strategy ?? 'balanced', sticky_ttl: value.sticky_ttl ?? '1h', max_attempts: value.max_attempts };
}

function pickResilience(value: any) {
  if (!isRecord(value)) return null;
  if (!optionalString(value.cooldown) || !optionalString(value.circuit_reset) || !optionalPositiveInteger(value.circuit_failures)) return null;
  return { cooldown: value.cooldown ?? '30s', circuit_failures: value.circuit_failures ?? 3, circuit_reset: value.circuit_reset ?? '1m' };
}

function containsUnexpectedCredentialMaterial(value: unknown, path: Array<string | number> = []): boolean {
  if (Array.isArray(value)) return value.some((item, index) => containsUnexpectedCredentialMaterial(item, [...path, index]));
  if (!isRecord(value)) return false;
  for (const [key, child] of Object.entries(value)) {
    const normalized = key.toLowerCase();
    const permittedLegacySecret = (path.length === 0 && normalized === 'secret') || (path[0] === 'accounts' && typeof path[1] === 'number' && normalized === 'api_key');
    const plaintextVirtualKey = path[0] === 'virtual_keys' && typeof path[1] === 'number' && normalized === 'key';
    if ((!permittedLegacySecret && ['secret', 'api_key', 'access_token', 'refresh_token', 'id_token', 'authorization'].includes(normalized)) || plaintextVirtualKey) return true;
    if (containsUnexpectedCredentialMaterial(child, [...path, key])) return true;
  }
  return false;
}

function isRecord(value: unknown): value is Record<string, any> { return !!value && typeof value === 'object' && !Array.isArray(value); }
function requiredString(value: unknown) { return typeof value === 'string' && value.length > 0; }
function optionalString(value: unknown) { return value === undefined || requiredString(value); }
function stringArray(value: unknown): value is string[] { return Array.isArray(value) && value.every(requiredString); }
function positiveInteger(value: unknown) { return Number.isInteger(value) && Number(value) > 0; }
function optionalPositiveInteger(value: unknown) { return value === undefined || positiveInteger(value); }

function legacyAccountMatches(legacy: any, row: any) {
  if (legacy.adapter && legacy.adapter !== row.adapter) return false;
  const legacyUrl = normalizeUrl(legacy.base_url);
  if (!legacyUrl) return false;
  return legacyUrl === normalizeUrl(row.base_url) || legacyUrl === normalizeUrl(row.provider_base_url);
}

function normalizeUrl(value: unknown) {
  if (typeof value !== 'string' || value.length === 0) return null;
  try {
    const u = new URL(value);
    u.hash = '';
    u.search = '';
    u.pathname = u.pathname.replace(/\/+$/, '');
    return u.toString().replace(/\/+$/, '').toLowerCase();
  } catch {
    return value.replace(/\/+$/, '').toLowerCase();
  }
}
