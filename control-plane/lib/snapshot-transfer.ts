import type { Pool, PoolClient } from 'pg';
import { audit, insertSecret, storeDraft } from './control';
import { decryptSecretJson } from './crypto';

type Queryable = Pool | PoolClient;

// Full declarative snapshot export/import, including encrypted credentials.
// Export is the admin's own backup/migration tool; the payload contains
// plaintext secrets and must be kept private.

type Row = Record<string, unknown>;

function b64(v: Buffer) { return v.toString('base64'); }
function rowToEncrypted(row: Row) {
  return {
    key_version: Number(row.key_version),
    data_key_nonce: b64(row.data_key_nonce as Buffer),
    data_key_ciphertext: b64(row.data_key_ciphertext as Buffer),
    data_key_tag: b64(row.data_key_tag as Buffer),
    secret_nonce: b64(row.secret_nonce as Buffer),
    secret_ciphertext: b64(row.secret_ciphertext as Buffer),
    secret_tag: b64(row.secret_tag as Buffer),
  };
}

export async function exportSnapshot(client: Queryable) {
  const providers = (await client.query('SELECT * FROM providers ORDER BY slug')).rows;
  const accountsRows = (await client.query(`SELECT a.*, p.slug provider_slug, sr.key_version, sr.data_key_nonce, sr.data_key_ciphertext, sr.data_key_tag, sr.secret_nonce, sr.secret_ciphertext, sr.secret_tag
    FROM accounts a
    JOIN providers p ON p.id=a.provider_id
    LEFT JOIN secret_records sr ON sr.id=a.secret_record_id ORDER BY a.name`)).rows;
  const accounts = accountsRows.map((row: Row) => {
    const credential = row.key_version ? decryptSecretJson<unknown>(rowToEncrypted(row)) : null;
    const out: Row = {
      id: row.id, provider_id: row.provider_id, provider_slug: row.provider_slug, name: row.name, display_name: row.display_name,
      base_url: row.base_url, enabled: row.enabled, priority: row.priority, weight: row.weight,
      max_concurrency: row.max_concurrency, cost: Number(row.cost), metadata: row.metadata,
    };
    if (credential) out.credential = credential;
    if (row.external_account_id) out.external_account_id = row.external_account_id;
    if (row.account_email) out.account_email = row.account_email;
    if (row.plan_type) out.plan_type = row.plan_type;
    if (row.token_expires_at) out.token_expires_at = row.token_expires_at;
    return out;
  });
  const modelRows = (await client.query(`SELECT ma.*, p.slug provider_slug, mp.input_per_mtok, mp.output_per_mtok FROM model_aliases ma LEFT JOIN model_pricing mp ON mp.model_alias_id=ma.id LEFT JOIN providers p ON p.id=ma.provider_id ORDER BY ma.alias`)).rows;
  const models = modelRows.map((row: Row) => ({
    id: row.id, alias: row.alias, upstream_model: row.upstream_model, provider_id: row.provider_id ?? null,
    provider_slug: row.provider_slug ?? null, routing_strategy: row.routing_strategy ?? null, enabled: row.enabled, fallbacks: row.fallbacks ?? [],
    input_per_mtok: Number(row.input_per_mtok ?? 0), output_per_mtok: Number(row.output_per_mtok ?? 0),
  }));
  const mappings = (await client.query(`SELECT mam.*, ma.alias, a.name account_name FROM model_account_mappings mam JOIN model_aliases ma ON ma.id=mam.model_alias_id JOIN accounts a ON a.id=mam.account_id ORDER BY ma.alias, mam.tier, mam.position`)).rows;
  const teams = (await client.query('SELECT * FROM teams ORDER BY name')).rows;
  const keysRows = (await client.query(`SELECT vk.*, t.budget_usd team_budget_usd FROM virtual_keys vk LEFT JOIN teams t ON t.id=vk.team_id ORDER BY vk.name`)).rows;
  const virtualKeys = keysRows.map((row: Row) => ({
    id: row.id, name: row.name, key_hash: row.key_hash, enabled: row.enabled, models: row.models ?? [],
    rpm: row.rpm, tpm: row.tpm ?? 0, max_concurrency: row.max_concurrency ?? 0, budget_usd: Number(row.budget_usd ?? 0),
    team_id: row.team_id ?? null, team_budget_usd: row.team_budget_usd ? Number(row.team_budget_usd) : undefined,
  }));
  const routing = (await client.query('SELECT * FROM routing_settings WHERE id=true')).rows[0] ?? null;
  const settings = (await client.query(`SELECT key, value FROM system_settings WHERE key IN ('optimization','webhook')`)).rows;
  return {
    version: 2,
    exported_at: new Date().toISOString(),
    providers,
    accounts,
    models,
    mappings,
    teams,
    virtual_keys: virtualKeys,
    routing,
    system_settings: settings,
  };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

// importSnapshot replaces the whole declarative state from an exported
// snapshot in one transaction. Credentials are re-encrypted on insert.
export async function importSnapshot(client: PoolClient, data: unknown): Promise<{ accounts: number; models: number; keys: number }> {
  if (!isRecord(data) || data.version !== 2) throw new Error('invalid_snapshot: expected version 2 export');
  const providers = Array.isArray(data.providers) ? data.providers : [];
  const accounts = Array.isArray(data.accounts) ? data.accounts : [];
  const models = Array.isArray(data.models) ? data.models : [];
  const mappings = Array.isArray(data.mappings) ? data.mappings : [];
  const teams = Array.isArray(data.teams) ? data.teams : [];
  const keys = Array.isArray(data.virtual_keys) ? data.virtual_keys : [];
  const routing = data.routing && isRecord(data.routing) ? data.routing : null;
  const settings = Array.isArray(data.system_settings) ? data.system_settings : [];

  if (accounts.length === 0) throw new Error('invalid_snapshot: no accounts');
  if (keys.length === 0) throw new Error('invalid_snapshot: at least one virtual key required');

  // Wipe current declarative state (dependents first).
  await client.query('DELETE FROM provider_operations');
  await client.query('DELETE FROM account_provider_state');
  await client.query('DELETE FROM discovered_models');
  await client.query('DELETE FROM model_account_mappings');
  await client.query('DELETE FROM model_pricing');
  await client.query('DELETE FROM model_aliases');
  await client.query('DELETE FROM accounts');
  await client.query('DELETE FROM secret_records');
  await client.query('DELETE FROM virtual_keys');
  await client.query('DELETE FROM teams');
  await client.query('DELETE FROM providers');

  const providerBySlug = new Map<string, string>();
  for (const provider of providers) {
    if (!isRecord(provider) || typeof provider.slug !== 'string' || typeof provider.name !== 'string' || typeof provider.base_url !== 'string') {
      throw new Error('invalid_snapshot: malformed provider');
    }
    const row = (await client.query('INSERT INTO providers (slug,name,adapter,base_url,enabled,metadata) VALUES ($1,$2,$3,$4,$5,$6) RETURNING id', [
      provider.slug, provider.name, provider.adapter ?? 'openai-compatible', provider.base_url,
      provider.enabled !== false, JSON.stringify(provider.metadata ?? {}),
    ])).rows[0];
    providerBySlug.set(provider.slug, row.id as string);
  }

  const teamById = new Map<string, string>();
  for (const team of teams) {
    if (!isRecord(team) || typeof team.name !== 'string') throw new Error('invalid_snapshot: malformed team');
    const row = (await client.query('INSERT INTO teams (name,enabled,budget_usd) VALUES ($1,$2,$3) RETURNING id', [team.name, team.enabled !== false, Number(team.budget_usd ?? 0)])).rows[0];
    teamById.set(String(team.id), row.id as string);
  }

  const accountById = new Map<string, string>();
  for (const account of accounts) {
    if (!isRecord(account) || typeof account.name !== 'string' || typeof account.base_url !== 'string') {
      throw new Error('invalid_snapshot: malformed account');
    }
    const secretId = isRecord(account.credential) ? await insertSecret(client, 'account_credential', account.credential) : null;
    const providerId = typeof account.provider_slug === 'string'
      ? providerBySlug.get(account.provider_slug)
      : null;
    if (!providerId) throw new Error(`invalid_snapshot: account ${account.name} has no resolvable provider`);
    const row = (await client.query(`INSERT INTO accounts
      (provider_id, secret_record_id, name, display_name, base_url, enabled, priority, weight, max_concurrency, cost, external_account_id, account_email, plan_type, token_expires_at, credential_persistence_status, credential_revision, metadata)
      VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,'persisted',1,$15) RETURNING id`, [
      providerId,
      secretId, account.name, account.display_name ?? account.name, account.base_url,
      account.enabled !== false, Number(account.priority ?? 1), Number(account.weight ?? 1),
      Number(account.max_concurrency ?? 100), Number(account.cost ?? 0),
      account.external_account_id ?? null, account.account_email ?? null, account.plan_type ?? null,
      account.token_expires_at ?? null, JSON.stringify(account.metadata ?? {}),
    ])).rows[0];
    accountById.set(String(account.id), row.id as string);
  }

  const modelById = new Map<string, string>();
  for (const model of models) {
    if (!isRecord(model) || typeof model.alias !== 'string' || typeof model.upstream_model !== 'string') {
      throw new Error('invalid_snapshot: malformed model');
    }
    // Provider-pool models are rebuilt by discovery; their accounts come from
    // discovered_models which is not part of the declarative export.
    if (model.provider_slug) continue;
    const providerId = typeof model.provider_slug === 'string' && model.provider_slug ? providerBySlug.get(model.provider_slug) ?? null : null;
    const row = (await client.query('INSERT INTO model_aliases (alias,upstream_model,provider_id,routing_strategy,enabled,fallbacks) VALUES ($1,$2,$3,$4,$5,$6) RETURNING id', [
      model.alias, model.upstream_model, providerId, model.routing_strategy ?? null,
      model.enabled !== false, Array.isArray(model.fallbacks) ? model.fallbacks : [],
    ])).rows[0];
    modelById.set(String(model.id), row.id as string);
    await client.query('INSERT INTO model_pricing (model_alias_id,input_per_mtok,output_per_mtok) VALUES ($1,$2,$3)',
      [row.id, Number(model.input_per_mtok ?? 0), Number(model.output_per_mtok ?? 0)]);
  }

  let mappingCount = 0;
  for (const mapping of mappings) {
    if (!isRecord(mapping)) continue;
    const modelId = modelById.get(String(mapping.model_alias_id));
    const accountId = accountById.get(String(mapping.account_id));
    if (!modelId || !accountId) continue;
    await client.query('INSERT INTO model_account_mappings (model_alias_id,account_id,enabled,tier,position) VALUES ($1,$2,$3,$4,$5)',
      [modelId, accountId, mapping.enabled !== false, Number(mapping.tier ?? 1), Number(mapping.position ?? 0)]);
    mappingCount++;
  }
  if (mappingCount === 0) throw new Error('invalid_snapshot: no model-account mappings');

  for (const key of keys) {
    if (!isRecord(key) || typeof key.name !== 'string' || typeof key.key_hash !== 'string') {
      throw new Error('invalid_snapshot: malformed virtual key');
    }
    await client.query(`INSERT INTO virtual_keys (name,key_hash,key_prefix,enabled,models,rpm,tpm,max_concurrency,budget_usd,team_id)
      VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, [
      key.name, key.key_hash, String(key.key_hash).slice(0, 10), key.enabled !== false,
      Array.isArray(key.models) ? key.models : [], Number(key.rpm ?? 60), Number(key.tpm ?? 0),
      Number(key.max_concurrency ?? 0), Number(key.budget_usd ?? 0), key.team_id ? teamById.get(String(key.team_id)) ?? null : null,
    ]);
  }

  if (routing) {
    await client.query(`INSERT INTO routing_settings (id,strategy,sticky_ttl,max_attempts,resilience) VALUES (true,$1,$2,$3,$4)
      ON CONFLICT (id) DO UPDATE SET strategy=EXCLUDED.strategy, sticky_ttl=EXCLUDED.sticky_ttl, max_attempts=EXCLUDED.max_attempts, resilience=EXCLUDED.resilience`,
      [routing.strategy ?? 'balanced', routing.sticky_ttl ?? '1h', Number(routing.max_attempts ?? 2), JSON.stringify(routing.resilience ?? {})]);
  }
  for (const setting of settings) {
    if (!isRecord(setting) || typeof setting.key !== 'string') continue;
    await client.query(`INSERT INTO system_settings (key,value,updated_at) VALUES ($1,$2,now()) ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=now()`,
      [setting.key, JSON.stringify(setting.value)]);
  }

  await storeDraft(client);
  await audit(client, 'snapshot_import', 'database', undefined, { accounts: accounts.length, models: models.length, keys: keys.length });
  return { accounts: accounts.length, models: models.length, keys: keys.length };
}
