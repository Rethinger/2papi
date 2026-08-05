import test from 'node:test';
import assert from 'node:assert/strict';
import { sanitizeHistoricalConfigVersions } from '../lib/snapshot-migration.ts';

function fakeClient(rows: any[] = []) {
  const calls: Array<{ sql: string; params?: unknown[] }> = [];
  const client = {
    query: async (sql: string, params?: unknown[]) => {
      calls.push({ sql, params });
      if (sql.includes('snapshot_migration_state WHERE migration')) return { rowCount: 0, rows: [] };
      if (sql.includes('config_versions ORDER BY version')) return { rows };
      if (sql.startsWith('SELECT a.*, p.adapter FROM accounts')) return { rows: [] };
      if (sql.startsWith('INSERT INTO snapshot_migration_state')) return { rows: [], rowCount: 1 };
      if (sql.startsWith('UPDATE config_versions')) return { rows: [], rowCount: 1 };
      return { rows: [], rowCount: 0 };
    },
  };
  return { client: client as any, calls };
}

test('snapshot migration marker is written after row updates in the same client sequence', async () => {
  const { client, calls } = fakeClient([{ version: 1, snapshot: { accounts: [] }, errors: [] }]);
  await sanitizeHistoricalConfigVersions(client);
  const updateAt = calls.findIndex(c => c.sql.startsWith('UPDATE config_versions'));
  const markerAt = calls.findIndex(c => c.sql.startsWith('INSERT INTO snapshot_migration_state'));
  assert.ok(updateAt >= 0);
  assert.ok(markerAt > updateAt);
});

test('snapshot migration does not write completion marker when rollback-worthy update fails', async () => {
  const { client, calls } = fakeClient([{ version: 1, snapshot: { accounts: [] }, errors: [] }]);
  const original = client.query.bind(client);
  client.query = async (sql: string, params?: unknown[]) => {
    if (sql.startsWith('UPDATE config_versions')) throw new Error('update failed');
    return original(sql, params);
  };
  await assert.rejects(() => sanitizeHistoricalConfigVersions(client), /update failed/);
  assert.equal(calls.some(c => c.sql.startsWith('INSERT INTO snapshot_migration_state')), false);
});

test('snapshot migration is idempotent when marker exists', async () => {
  const calls: string[] = [];
  const client = { query: async (sql: string) => {
    calls.push(sql);
    if (sql.includes('pg_advisory_xact_lock')) return { rowCount: 1, rows: [] };
    if (sql.includes('snapshot_migration_state WHERE migration')) return { rowCount: 1, rows: [{ '?column?': 1 }] };
    throw new Error(`unexpected query ${sql}`);
  } } as any;
  const result = await sanitizeHistoricalConfigVersions(client);
  assert.deepEqual(result, { skipped: true });
  assert.equal(calls.some(sql => sql.includes('config_versions ORDER BY version')), false);
});
