import fs from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { pool } from '../lib/db';
import { sanitizeHistoricalConfigVersions } from '../lib/snapshot-migration';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
await pool.query('CREATE TABLE IF NOT EXISTS schema_migrations (name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())');
for (const name of (await fs.readdir(path.join(root, 'migrations'))).filter(n => n.endsWith('.sql')).sort()) {
  const seen = await pool.query('SELECT 1 FROM schema_migrations WHERE name=$1', [name]);
  if (seen.rowCount) continue;
  const sql = await fs.readFile(path.join(root, 'migrations', name), 'utf8');
  await pool.query('BEGIN');
  try { await pool.query(sql); await pool.query('INSERT INTO schema_migrations (name) VALUES ($1)', [name]); await pool.query('COMMIT'); console.log(`applied ${name}`); }
  catch (e) { await pool.query('ROLLBACK'); throw e; }
}
const client = await pool.connect();
await client.query('BEGIN');
try { await sanitizeHistoricalConfigVersions(client); await client.query('COMMIT'); console.log('sanitized config history'); }
catch (e) { await client.query('ROLLBACK'); throw e; }
finally { client.release(); }
await pool.end();
