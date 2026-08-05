import fs from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { pool } from '../lib/db';
import { sanitizeHistoricalConfigVersions } from '../lib/snapshot-migration';

export async function runMigrations(acquiredPool = pool) {
  const client = await acquiredPool.connect();
  try {
    await client.query('CREATE TABLE IF NOT EXISTS schema_migrations (name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())');
    const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
    for (const name of (await fs.readdir(path.join(root, 'migrations'))).filter(n => n.endsWith('.sql')).sort()) {
      const seen = await client.query('SELECT 1 FROM schema_migrations WHERE name=$1', [name]);
      if (seen.rowCount) continue;
      const sql = await fs.readFile(path.join(root, 'migrations', name), 'utf8');
      await client.query('BEGIN');
      try {
        await client.query(sql);
        await client.query('INSERT INTO schema_migrations (name) VALUES ($1)', [name]);
        await client.query('COMMIT');
        console.log(`applied ${name}`);
      } catch (e) {
        await client.query('ROLLBACK');
        throw e;
      }
    }
    await client.query('BEGIN');
    try {
      await sanitizeHistoricalConfigVersions(client);
      await client.query('COMMIT');
      console.log('sanitized config history');
    } catch (e) {
      await client.query('ROLLBACK');
      throw e;
    }
  } finally {
    client.release();
  }
}

if (process.argv[1]?.replace(/\\/g, '/').endsWith('/scripts/migrate.ts')) {
  await runMigrations();
  await pool.end();
}
