import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import { Pool } from 'pg';
import { probeAccountCredentials, probeAllAccounts } from '../lib/credential-prober.ts';

const url = process.env.TEST_DATABASE_URL;
const options = { skip: url ? false : 'TEST_DATABASE_URL is not set' };
const pool = url ? new Pool({ connectionString: url, max: 2 }) : null;

test.before(async () => {
  if (!pool) return;
  await pool.query('DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;');
  const dir = path.join(process.cwd(), 'migrations');
  for (const name of (await fs.readdir(dir)).filter(name => name.endsWith('.sql')).sort()) {
    await pool.query(await fs.readFile(path.join(dir, name), 'utf8'));
  }
});

test.after(async () => {
  await pool?.end();
});

test('probeAccountCredentials records success and failure states', options, async () => {
  const prov = (await pool!.query(`INSERT INTO providers (slug, name, adapter, base_url, enabled)
    VALUES ('probe-prov', 'Probe Prov', 'openai-compatible', 'http://fake:9001', true) RETURNING id`)).rows[0];
  const account = (await pool!.query(`INSERT INTO accounts (provider_id, name, display_name, base_url, enabled, priority, weight, max_concurrency, cost)
    VALUES ($1, 'probe-acct', 'Probe Acct', 'http://fake:9001', true, 1, 1, 100, 0) RETURNING id`, [prov.id])).rows[0];

  // Success path
  const okResult = await probeAccountCredentials(pool!, account.id, {
    dispatch: async () => ({ data: { valid: true } }),
  });
  assert.equal(okResult.status, 'ok');

  const okState = (await pool!.query(
    "SELECT last_operation, last_error_code FROM account_provider_state WHERE account_id=$1",
    [account.id],
  )).rows[0];
  assert.equal(okState.last_operation, 'credential.probe');
  assert.equal(okState.last_error_code, null);

  // Failure path
  const failResult = await probeAccountCredentials(pool!, account.id, {
    dispatch: async () => { throw new Error('credentials revoked'); },
  });
  assert.equal(failResult.status, 'failed');
  assert.equal(failResult.error_code, 'provider_operation_failed');

  const failState = (await pool!.query(
    "SELECT last_operation, last_error_code FROM account_provider_state WHERE account_id=$1",
    [account.id],
  )).rows[0];
  assert.equal(failState.last_operation, 'credential.probe');
  assert.ok(failState.last_error_code, 'error code should be recorded');

  const auditCount = (await pool!.query(
    "SELECT count(*)::int n FROM audit_events WHERE action='credential.probe' AND resource_id=$1",
    [account.id],
  )).rows[0].n;
  assert.equal(auditCount, 2);
});

test('probeAllAccounts skips disabled accounts', options, async () => {
  const prov = (await pool!.query("SELECT id FROM providers WHERE slug='probe-prov'")).rows[0];
  await pool!.query(`INSERT INTO accounts (provider_id, name, display_name, base_url, enabled, priority, weight, max_concurrency, cost)
    VALUES ($1, 'probe-disabled', 'Probe Disabled', 'http://fake:9001', false, 1, 1, 100, 0)`, [prov.id]);

  const results = await probeAllAccounts(pool!, {
    dispatch: async () => ({ data: {} }),
  });
  assert.equal(results.length, 1, 'only the enabled account should be probed');
  assert.equal(results[0].account_name, 'probe-acct');
});
