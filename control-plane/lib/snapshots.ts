import type { PoolClient } from 'pg';
import { env } from './env';
import { sha256Canonical } from './canonical-json';
import { decryptSecretJson, type EncryptedSecretRecord } from './crypto';
import { normalizeProxy, parseProxyList } from './proxylib';

export type CompiledDeclarativeSnapshot = { snapshot: any; checksum: string; schemaVersion: number };
export type RuntimeSnapshot = any;

export function credentialDigestFromDeclarative(snapshot: any) {
  const identity = (snapshot.accounts ?? [])
    .map((account: any) => ({ id: account.id ?? account.name, credential_revision: Number(account.credential_revision ?? 1) }))
    .sort((a: any, b: any) => String(a.id).localeCompare(String(b.id)));
  return sha256Canonical(identity);
}

export async function credentialDigestForDeclarative(client: PoolClient, declarative: any) {
  const accountIds = (declarative.accounts ?? []).map((account: any) => account.id);
  if (accountIds.length === 0) return credentialDigestFromDeclarative(declarative);
  const rows = await client.query('SELECT id,credential_revision FROM accounts WHERE id = ANY($1::uuid[])', [accountIds]);
  if (rows.rows.length !== accountIds.length) throw new Error('snapshot account credential identity is incomplete');
  return credentialDigestFromDeclarative({ accounts: rows.rows });
}

export async function compileDeclarativeSnapshot(client: PoolClient): Promise<CompiledDeclarativeSnapshot> {
  const accountsR = await client.query(`SELECT a.*, p.adapter FROM accounts a JOIN providers p ON p.id=a.provider_id WHERE a.enabled ORDER BY a.priority, a.name`);
  const modelsR = await client.query(`SELECT ma.*, mp.input_per_mtok, mp.output_per_mtok
    FROM model_aliases ma LEFT JOIN model_pricing mp ON mp.model_alias_id = ma.id
    WHERE ma.enabled ORDER BY ma.alias`);
  const mapsR = await client.query(`SELECT mam.*, a.name account_name, ma.alias FROM model_account_mappings mam JOIN accounts a ON a.id=mam.account_id JOIN model_aliases ma ON ma.id=mam.model_alias_id WHERE mam.enabled AND a.enabled ORDER BY mam.tier, mam.position`);
  const providerPoolsR = await client.query(
    `SELECT ma.alias,a.name account_name
     FROM model_aliases ma
     JOIN accounts a ON a.provider_id=ma.provider_id AND a.enabled=true
     JOIN discovered_models dm ON dm.provider_id=ma.provider_id AND dm.account_id=a.id AND dm.upstream_model=ma.upstream_model AND dm.available=true
     WHERE ma.enabled=true AND ma.provider_id IS NOT NULL
     ORDER BY ma.alias,a.priority,a.name`,
  );
  const routingR = await client.query('SELECT * FROM routing_settings WHERE id=true');
  const settingsR = await client.query(`SELECT key, value FROM system_settings WHERE key IN ('optimization','webhook','proxy_pool')`);
  const settingsByKey = new Map(settingsR.rows.map((row: any) => [row.key, row.value]));
  const optimization = settingsByKey.get('optimization') ?? { rtk_compression: false, caveman: false, headroom: false, headroom_reserve: 120000, headroom_keep: 8 };
  const webhookValue = settingsByKey.get('webhook');
  const proxyPoolValue = settingsByKey.get('proxy_pool');
  const proxyPool = typeof proxyPoolValue?.raw === 'string' && proxyPoolValue.raw.trim()
    ? parseProxyList(proxyPoolValue.raw).entries.map(normalizeProxy)
    : [];
  const keysR = await client.query(`SELECT vk.*, t.id team_id, t.budget_usd team_budget_usd, o.id org_id, o.budget_usd org_budget_usd
    FROM virtual_keys vk
    LEFT JOIN teams t ON t.id=vk.team_id
    LEFT JOIN organizations o ON o.id=t.org_id
    WHERE vk.enabled ORDER BY vk.name`);
  const teamKeyCountsR = await client.query(`SELECT team_id, count(*)::int key_count
    FROM virtual_keys WHERE enabled=true AND team_id IS NOT NULL GROUP BY team_id`);
  const teamKeyCounts = new Map(teamKeyCountsR.rows.map((row: any) => [row.team_id, Number(row.key_count)]));
  const accounts = accountsR.rows.map((account: any) => ({
    id: account.id,
    name: account.name,
    adapter: account.adapter,
    base_url: account.base_url,
    credential_revision: Number(account.credential_revision ?? 1),
    enabled: account.enabled,
    priority: account.priority,
    weight: account.weight,
    max_concurrency: account.max_concurrency,
    cost: Number(account.cost),
    ...(typeof account.metadata?.proxy === 'string' && account.metadata.proxy.trim() ? { proxy: account.metadata.proxy } : {}),
  }));
  const byAlias = new Map<string, string[]>();
  for (const m of mapsR.rows) byAlias.set(m.alias, [...(byAlias.get(m.alias) ?? []), m.account_name]);
  const providerByAlias = new Map<string, string[]>();
  for (const m of providerPoolsR.rows) providerByAlias.set(m.alias, [...(providerByAlias.get(m.alias) ?? []), m.account_name]);
  const models = modelsR.rows.map((m: any) => {
    const accountNames = m.provider_id ? providerByAlias.get(m.alias) ?? [] : byAlias.get(m.alias) ?? [];
    if (accountNames.length === 0) throw new Error(`model ${m.alias} has no eligible accounts`);
    return {
      alias: m.alias,
      upstream_model: m.upstream_model,
      accounts: accountNames,
      ...(m.provider_id ? { routing_strategy: m.routing_strategy } : {}),
      ...(Array.isArray(m.fallbacks) && m.fallbacks.length > 0 ? { fallbacks: m.fallbacks } : {}),
      ...(Number(m.input_per_mtok) > 0 ? { input_cost_per_mtok: Number(m.input_per_mtok) } : {}),
      ...(Number(m.output_per_mtok) > 0 ? { output_cost_per_mtok: Number(m.output_per_mtok) } : {}),
    };
  });
  validateFallbackChains(models);
  const routing = routingR.rows[0] ?? { strategy: 'balanced', sticky_ttl: '1h', max_attempts: 2, resilience: { cooldown: '30s', circuit_failures: 3, circuit_reset: '1m', lockout_failures: 10, lockout_duration: '15m' } };
  const resilience = {
    cooldown: routing.resilience?.cooldown ?? '30s',
    circuit_failures: Number(routing.resilience?.circuit_failures ?? 3),
    circuit_reset: routing.resilience?.circuit_reset ?? '1m',
    lockout_failures: Number(routing.resilience?.lockout_failures ?? 10),
    lockout_duration: routing.resilience?.lockout_duration ?? '15m',
  };
  const snapshot = { version: 2, metadata: {}, server: { addr: ':8080', read_timeout: '10s', write_timeout: '0s' }, virtual_keys: keysR.rows.map((k: any) => ({
    name: k.name,
    ...(k.id ? { id: k.id } : {}),
    key_hash: k.key_hash,
    models: k.models,
    rpm: k.rpm,
    ...(Number(k.tpm) > 0 ? { tpm: Number(k.tpm) } : {}),
    ...(Number(k.max_concurrency) > 0 ? { max_concurrency: Number(k.max_concurrency) } : {}),
    ...(Number(k.budget_usd) > 0 ? { budget_usd: Number(k.budget_usd) } : {}),
    ...(k.team_id && (Number(k.team_budget_usd) > 0 || Number(k.org_budget_usd) > 0) ? (() => {
      const share = Number(k.team_budget_usd) / (teamKeyCounts.get(k.team_id) ?? 1);
      return { team: {
        id: k.team_id,
        budget_usd: Number(k.team_budget_usd),
        ...(share > 0 ? { share_usd: Math.round(share * 1e6) / 1e6 } : {}),
        // Org budget caps every team under it (enforced in internal/policy);
        // emitted only when the org actually has one.
        ...(k.org_id && Number(k.org_budget_usd) > 0 ? { org: { id: k.org_id, budget_usd: Number(k.org_budget_usd) } } : {}),
      } };
    })() : {}),
  })), models, accounts, ...(proxyPool.length > 0 ? { proxies: proxyPool } : {}), routing: { strategy: routing.strategy, sticky_ttl: routing.sticky_ttl, max_attempts: routing.max_attempts }, resilience, optimization: { rtk_compression: Boolean(optimization.rtk_compression), caveman: Boolean(optimization.caveman), headroom: Boolean(optimization.headroom), headroom_reserve: Number(optimization.headroom_reserve) || 120000, headroom_keep: Number(optimization.headroom_keep) || 8 }, ...(webhookValue ? { webhook: { enabled: Boolean(webhookValue.enabled), url: typeof webhookValue.url === 'string' ? webhookValue.url : '', secret: typeof webhookValue.secret === 'string' ? webhookValue.secret : '' } } : {}) };
  if (snapshot.virtual_keys.length === 0) throw new Error('at least one virtual key required');
  return { snapshot, checksum: sha256Canonical(snapshot), schemaVersion: 2 };
}

function validateFallbackChains(models: Array<{ alias: string; fallbacks?: string[] }>) {
  const byAlias = new Map(models.map(model => [model.alias, model]));
  for (const model of models) {
    for (const fallback of model.fallbacks ?? []) {
      if (!byAlias.has(fallback)) {
        throw new Error(`model ${model.alias} fallback references unknown model ${fallback}`);
      }
    }
    const path = new Set<string>();
    let current: string | undefined = model.alias;
    while (current) {
      if (path.has(current)) {
        throw new Error(`model fallback cycle involving ${current}`);
      }
      path.add(current);
      const next = byAlias.get(current);
      current = next?.fallbacks?.[0];
    }
  }
}

async function credentialByAccountId(client: PoolClient, accountIds: string[]) {
  const rows = await client.query(`SELECT a.id account_id, a.credential_revision, sr.* FROM accounts a JOIN secret_records sr ON sr.id=a.secret_record_id WHERE a.id = ANY($1::uuid[])`, [accountIds]);
  return new Map(rows.rows.map((row: any) => [row.account_id, { credential: decryptSecretJson<any>(rowToEncrypted(row)), revision: Number(row.credential_revision ?? 1) }]));
}

export async function materializeRuntimeSnapshot(client: PoolClient, declarative: any): Promise<RuntimeSnapshot> {
  const accountIds = (declarative.accounts ?? []).map((a: any) => a.id);
  const secrets = await credentialByAccountId(client, accountIds);
  const accounts = (declarative.accounts ?? []).map((a: any) => {
    const current = secrets.get(a.id);
    if (!current) throw new Error(`account ${a.id} missing credential`);
    const kind = current.credential.kind ?? (a.adapter === 'openai-codex' ? 'oauth' : 'api_key');
    return { ...a, credential_revision: current.revision, credential: { ...current.credential, kind, revision: current.revision } };
  });
  return { ...declarative, version: 2, secret: env.GATEWAY_SHARED_SECRET, accounts };
}

export async function materializeLegacyRuntimeSnapshot(client: PoolClient, declarative: any): Promise<RuntimeSnapshot> {
  const accountIds = (declarative.accounts ?? []).map((a: any) => a.id);
  const secrets = await credentialByAccountId(client, accountIds);
  const accounts = (declarative.accounts ?? []).map((a: any) => {
    const current = secrets.get(a.id);
    if (!current?.credential?.api_key) throw new Error(`account ${a.id} missing credential`);
    return { name: a.name, base_url: a.base_url, api_key: current.credential.api_key, enabled: a.enabled, priority: a.priority, weight: a.weight, max_concurrency: a.max_concurrency, cost: a.cost, ...(a.proxy ? { proxy: a.proxy } : {}) };
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

export const FORBIDDEN_SNAPSHOT_PATTERN = /integration-secret|dev-secret-change-me|api_key|access_token|refresh_token|id_token|cookie|session_key/i;
