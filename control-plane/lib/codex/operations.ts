import type { Pool, PoolClient } from 'pg';
import { z } from 'zod';
import { ApiError } from '../api';
import { dispatchProviderOperation } from '../provider-operations';
import { audit, storeDraft } from '../control';
import type { Queryable } from '../db';
import { mergeModelMetadata, normalizeModelMetadata } from '../model-metadata';

const METADATA_LIMIT = 32 * 1024;
const CONCURRENCY = 4;
const ALIAS_RE = /^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$/;
const TRAILING_SEPARATOR_RE = /[._:\/-]$/;
const SECRET_METADATA_KEY_PARTS = ['oauth', 'token', 'apikey', 'authorization', 'cookie', 'clientsecret', 'password', 'privatekey', 'secret', 'credential', 'bearer', 'session'];

type GatewayOperation = (account: AccountRow, client: Queryable) => Promise<{ data: unknown; warning_code?: string }>;

export type DiscoveryScope = { scope: 'all' } | { scope: 'provider_id'; provider_id: string } | { scope: 'account_id'; account_id: string };

type AccountRow = { id: string; provider_id: string; name: string; display_name: string; adapter: string; base_url: string; enabled: boolean };

type ParsedModel = { upstream_model: string; display_name: string; capabilities: Record<string, unknown>; visibility: string; supported_in_api: boolean; raw_metadata: unknown };

const DiscoveryModelSchema = z.object({
  slug: z.string().min(1).optional(),
  id: z.string().min(1).optional(),
  name: z.string().min(1).optional(),
  display_name: z.string().min(1).optional(),
  capabilities: z.record(z.string(), z.unknown()).optional(),
  visibility: z.string().optional(),
  supported_in_api: z.boolean().optional(),
}).passthrough();

export function validatePublicAlias(alias: string) {
  if (!ALIAS_RE.test(alias) || TRAILING_SEPARATOR_RE.test(alias) || /\s|[\x00-\x1f\x7f]/.test(alias)) {
    throw new ApiError(400, 'invalid_model_alias', 'Model alias must match public alias rules exactly');
  }
  return alias;
}

export async function gatewayDiscoverModels(account: AccountRow, client: Queryable): Promise<{ data: unknown; warning_code?: string }> {
  return dispatchProviderOperation(client, account.id, 'discover_models', {}, `discover-models:${account.id}:${Date.now()}`);
}

export async function discoverModelsForScope(client: Queryable, scope: DiscoveryScope, deps: { gatewayOperation?: GatewayOperation } = {}) {
  const accounts = await accountsForScope(client, scope);
  const gatewayOperation = deps.gatewayOperation ?? gatewayDiscoverModels;
  const results = await mapLimit(accounts, CONCURRENCY, async account => {
    try {
      const response = await gatewayOperation(account, client);
      const models = parseModels(response.data);
      await persistAccountModelsAtomic(client, account, models);
      return { account_id: account.id, account_name: account.name, status: 'succeeded' as const, model_count: models.length, warning_code: response.warning_code ?? null };
    } catch (error) {
      return { account_id: account.id, account_name: account.name, status: 'failed' as const, error: safeError(error) };
    }
  });
  await audit(client as PoolClient, 'discover_models', 'model_discovery', undefined, { scope, succeeded: results.filter(r => r.status === 'succeeded').length, failed: results.filter(r => r.status === 'failed').length });
  return { scope: scope.scope, results };
}

async function accountsForScope(client: Queryable, scope: DiscoveryScope): Promise<AccountRow[]> {
  const base = `SELECT a.id,a.provider_id,a.name,a.display_name,a.base_url,a.enabled,p.adapter FROM accounts a JOIN providers p ON p.id=a.provider_id WHERE a.enabled=true`;
  if (scope.scope === 'all') return (await client.query(`${base} ORDER BY a.name`)).rows;
  if (scope.scope === 'provider_id') return (await client.query(`${base} AND a.provider_id=$1 ORDER BY a.name`, [scope.provider_id])).rows;
  return (await client.query(`${base} AND a.id=$1 ORDER BY a.name`, [scope.account_id])).rows;
}

function parseModels(data: unknown): ParsedModel[] {
  const raw: unknown[] = Array.isArray((data as any)?.models)
    ? (data as any).models
    : Array.isArray((data as any)?.data)
      ? (data as any).data
      : Array.isArray(data)
        ? data
        : [];
  return raw.map(item => {
    const parsed = DiscoveryModelSchema.parse(item);
    const upstream = parsed.slug ?? parsed.id ?? parsed.name;
    if (!upstream) throw new ApiError(502, 'provider_model_missing_slug', 'Discovered model is missing slug, id, or name');
    return { upstream_model: upstream, display_name: parsed.display_name ?? upstream, capabilities: parsed.capabilities ?? {}, visibility: parsed.visibility ?? 'unknown', supported_in_api: parsed.supported_in_api ?? false, raw_metadata: sanitizeMetadata(parsed) };
  });
}

function sanitizeMetadata(input: unknown) {
  const filtered = filterSecretMetadata(input ?? {});
  let text = JSON.stringify(filtered);
  if (Buffer.byteLength(text) <= METADATA_LIMIT) return filtered;
  const out: Record<string, unknown> = { truncated: true };
  for (const [key, value] of Object.entries((filtered as any) ?? {})) {
    const next = JSON.stringify({ ...out, [key]: value });
    if (Buffer.byteLength(next) > METADATA_LIMIT) continue;
    out[key] = value;
  }
  text = JSON.stringify(out);
  while (Buffer.byteLength(text) > METADATA_LIMIT) {
    delete out[Object.keys(out).at(-1)!];
    text = JSON.stringify(out);
  }
  return out;
}

function filterSecretMetadata(input: unknown): unknown {
  if (Array.isArray(input)) return input.map(item => filterSecretMetadata(item));
  if (!input || typeof input !== 'object') return input;
  const out: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(input as Record<string, unknown>)) {
    if (isSecretMetadataKey(key)) continue;
    out[key] = filterSecretMetadata(value);
  }
  return out;
}

function isSecretMetadataKey(key: string) {
  const normalized = key.replace(/[^a-z0-9]/gi, '').toLowerCase();
  return SECRET_METADATA_KEY_PARTS.some(part => normalized.includes(part));
}

async function persistAccountModelsAtomic(client: Queryable, account: AccountRow, models: ParsedModel[]) {
  if (isPool(client)) {
    const connection = await client.connect();
    try {
      await connection.query('BEGIN');
      await persistAccountModels(connection, account, models);
      await connection.query('COMMIT');
    } catch (error) {
      await connection.query('ROLLBACK');
      throw error;
    } finally {
      connection.release();
    }
    return;
  }
  await persistAccountModels(client, account, models);
}

async function persistAccountModels(client: PoolClient, account: AccountRow, models: ParsedModel[]) {
  const seen = models.map(m => m.upstream_model);
  for (const model of models) {
    await client.query(
      `INSERT INTO discovered_models (provider_id,account_id,upstream_model,display_name,capabilities,visibility,supported_in_api,available,raw_metadata,last_seen_at)
       VALUES ($1,$2,$3,$4,$5,$6,$7,true,$8,now())
       ON CONFLICT (account_id, upstream_model) DO UPDATE SET display_name=EXCLUDED.display_name, capabilities=EXCLUDED.capabilities, visibility=EXCLUDED.visibility, supported_in_api=EXCLUDED.supported_in_api, available=true, raw_metadata=EXCLUDED.raw_metadata, last_seen_at=now()`,
      [account.provider_id, account.id, model.upstream_model, model.display_name, JSON.stringify(model.capabilities), model.visibility, model.supported_in_api, JSON.stringify(model.raw_metadata)],
    );
  }
  await client.query('UPDATE discovered_models SET available=false WHERE account_id=$1 AND NOT (upstream_model = ANY($2::text[]))', [account.id, seen]);
}

export async function groupedDiscoveredModels(client: Queryable) {
  const rows = await client.query(
    `SELECT dm.provider_id,p.slug provider_slug,p.name provider_name,p.adapter,
            dm.upstream_model,dm.display_name,dm.supported_in_api,dm.capabilities,
            dm.raw_metadata,dm.account_id,dm.available,dm.last_seen_at,a.display_name account_display_name
     FROM discovered_models dm
     JOIN providers p ON p.id=dm.provider_id
     JOIN accounts a ON a.id=dm.account_id
     ORDER BY p.name,p.id,dm.upstream_model,a.priority,a.name`,
  );
  const grouped = new Map<string, any>();
  for (const row of rows.rows) {
    const key = `${row.provider_id}:${row.upstream_model}`;
    const group = grouped.get(key) ?? {
      provider_id: row.provider_id, provider_slug: row.provider_slug, provider_name: row.provider_name,
      adapter: row.adapter, upstream_model: row.upstream_model, display_name: row.display_name,
      accounts: {}, account_count: 0, available_account_count: 0, metadata_items: [],
    };
    group.accounts[row.account_id] = { available: row.available, display_name: row.account_display_name, last_seen_at: row.last_seen_at };
    group.account_count++;
    if (row.available) {
      group.available_account_count++;
      group.metadata_items.push(normalizeModelMetadata({ ...record(row.raw_metadata), capabilities: row.capabilities, supported_in_api: row.supported_in_api }));
    }
    grouped.set(key, group);
  }
  return [...grouped.values()].map(group => {
    const { metadata_items, ...model } = group;
    const metadata = mergeModelMetadata(metadata_items);
    return { ...model, metadata, supported_in_api: metadata.supported_in_api };
  });
}

export async function importSelection(client: PoolClient, input: { alias: string; upstream_model: string; account_ids: string[]; enabled?: boolean }) {
  const alias = validatePublicAlias(input.alias);
  await assertAliasAvailable(client, alias);
  const inserted = await client.query('INSERT INTO model_aliases (alias,upstream_model,enabled) VALUES ($1,$2,$3) RETURNING *', [alias, input.upstream_model, input.enabled ?? true]);
  for (let i = 0; i < input.account_ids.length; i++) {
    await client.query('INSERT INTO model_account_mappings (model_alias_id,account_id,position) VALUES ($1,$2,$3)', [inserted.rows[0].id, input.account_ids[i], i]);
  }
  await audit(client, 'import_selection', 'model_alias', inserted.rows[0].id, { alias, upstream_model: input.upstream_model, account_count: input.account_ids.length });
  await storeDraft(client);
  return inserted.rows[0];
}

export async function renameModelAlias(client: PoolClient, id: string, alias: string, deps: { afterAliasUpdate?: () => Promise<void> } = {}) {
  const next = validatePublicAlias(alias);
  const current = await client.query('SELECT alias FROM model_aliases WHERE id=$1 FOR UPDATE', [id]);
  if (!current.rows[0]) throw new ApiError(404, 'not_found', 'Model alias not found');
  const old = current.rows[0].alias as string;
  if (old.toLowerCase() !== next.toLowerCase()) await assertAliasAvailable(client, next);
  const renamed = await client.query('UPDATE model_aliases SET alias=$2, updated_at=now() WHERE id=$1 RETURNING *', [id, next]);
  await deps.afterAliasUpdate?.();
  await client.query(`UPDATE virtual_keys SET models = ARRAY(SELECT CASE WHEN m=$1 THEN $2 ELSE m END FROM unnest(models) AS m) WHERE $1 = ANY(models)`, [old, next]);
  await audit(client, 'rename', 'model_alias', id, { old_alias: old, new_alias: next });
  await storeDraft(client);
  return renamed.rows[0];
}

async function assertAliasAvailable(client: PoolClient, alias: string) {
  const conflict = await client.query('SELECT id FROM model_aliases WHERE lower(alias)=lower($1) LIMIT 1', [alias]);
  if (conflict.rows[0]) throw new ApiError(409, 'model_alias_conflict', 'Model alias conflicts case-insensitively with an existing alias');
}

async function mapLimit<T, R>(items: T[], limit: number, fn: (item: T) => Promise<R>) {
  const out: R[] = new Array(items.length);
  let next = 0;
  const workers = Array.from({ length: Math.min(limit, items.length) }, async () => {
    while (next < items.length) {
      const index = next++;
      out[index] = await fn(items[index]);
    }
  });
  await Promise.all(workers);
  return out;
}

function isPool(client: Queryable): client is Pool {
  return typeof (client as Pool).connect === 'function' && typeof (client as PoolClient).release !== 'function';
}

function safeError(error: unknown) { return { code: error instanceof ApiError ? error.code : 'provider_operation_failed', message: 'Provider operation failed' }; }
function safeMessage(message: string) { return message.replace(/[\x00-\x1f\x7f]/g, '').slice(0, 512); }
function record(value: unknown): Record<string, unknown> { return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}; }
