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
    if (result.ok) {
      const checksum = sha256Canonical(result.snapshot);
      if (FORBIDDEN_SNAPSHOT_PATTERN.test(JSON.stringify(result.snapshot))) throw new Error('forbidden credential survived snapshot reconstruction');
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
    const accounts = [];
    for (const a of legacy.accounts) {
      if (!a?.name || !a?.base_url) return { ok: false };
      const r = await client.query(`SELECT a.*, p.adapter FROM accounts a JOIN providers p ON p.id=a.provider_id WHERE a.name=$1`, [a.name]);
      if (!r.rows[0]) return { ok: false };
      const row = r.rows[0];
      accounts.push({ id: row.id, name: row.name, adapter: row.adapter ?? 'openai-compatible', base_url: row.base_url, credential_revision: Number(row.credential_revision ?? 1), enabled: row.enabled, priority: row.priority, weight: row.weight, max_concurrency: row.max_concurrency, cost: Number(row.cost) });
    }
    return { ok: true, snapshot: { version: 2, metadata: legacy.metadata ?? {}, server: legacy.server, virtual_keys: legacy.virtual_keys, models: legacy.models, accounts, routing: legacy.routing, resilience: legacy.resilience } };
  } catch { return { ok: false }; }
}
