import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import { Pool } from 'pg';

// Route handlers bind lib/db's pool to DATABASE_URL at import time — point
// it at the test database BEFORE the route module is imported below.
const url = process.env.TEST_DATABASE_URL;
const options = { skip: url ? false : 'TEST_DATABASE_URL is not set' };
if (url) process.env.DATABASE_URL = url;
const pool = url ? new Pool({ connectionString: url, max: 2 }) : null;

// eslint-disable-next-line @typescript-eslint/no-explicit-any
let route: any;

test.before(async () => {
  if (!pool) return;
  await pool.query('DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;');
  const dir = path.join(process.cwd(), 'migrations');
  for (const name of (await fs.readdir(dir)).filter(name => name.endsWith('.sql')).sort()) {
    await pool.query(await fs.readFile(path.join(dir, name), 'utf8'));
  }
  await pool.query(
    `INSERT INTO virtual_keys (name, key_hash, key_prefix, models, rpm)
     VALUES ('seed-key', $1, 'sk-seed', ARRAY['model-a'], 60)`,
    ['a'.repeat(64)],
  );
  await pool.query(`INSERT INTO users (email, password_hash, platform_role) VALUES ('op@test.local', 'x', 'operator')`);
  route = await import('../app/api/control/v1/[[...resource]]/route.ts');
  const { createSession } = await import('../lib/auth.ts');
  const opUser = (await pool.query(`SELECT id FROM users WHERE email='op@test.local'`)).rows[0];
  const session = await createSession(pool, opUser.id);
  operatorCookie = `papi_session=${session.token}`;
});

test.after(async () => {
  await pool?.end();
});

const EDITION_ENV = '2PAPI_EDITION';
let operatorCookie = '';

function call(method: 'GET' | 'POST' | 'PATCH' | 'DELETE', resource: string[], body?: unknown, headers: Record<string, string> = {}) {
  const req = new Request(`http://localhost/api/control/v1/${resource.join('/')}`, {
    method,
    headers: { 'content-type': 'application/json', cookie: operatorCookie, ...headers },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  });
  return route[method](req, { params: Promise.resolve({ resource }) });
}

test('audit-export sleeps without a license and works under env override', options, async () => {
  // Seed one audit row so the export has content.
  await pool!.query(
    `INSERT INTO audit_events (actor, action, resource_type, resource_id) VALUES ('tester','export-probe','team','t-1')`,
  );

  delete process.env[EDITION_ENV]; // OSS: feature asleep.
  try {
    const res = await call('GET', ['audit-export']);
    assert.equal(res.status, 403);
    assert.equal((await res.json()).error.code, 'feature_not_licensed');
  } finally {
    delete process.env[EDITION_ENV];
  }

  process.env[EDITION_ENV] = 'cloud';
  try {
    const res = await call('GET', ['audit-export']);
    assert.equal(res.status, 200);
    assert.match(res.headers.get('content-type') ?? '', /application\/x-ndjson/);
    const lines = (await res.text()).trim().split('\n').map((line: string) => JSON.parse(line));
    assert.ok(lines.some((row: any) => row.action === 'export-probe'));
  } finally {
    delete process.env[EDITION_ENV];
  }
});

test('ipacl: validation, persistence and XFF enforcement on control API', options, async () => {
  process.env[EDITION_ENV] = 'cloud';
  try {
    // Invalid CIDR rejected before touching storage.
    const bad = await call('POST', ['ipacl'], { cidrs: ['999.0.0.0/8'] });
    assert.equal(bad.status, 400);
    assert.equal((await bad.json()).error.code, 'invalid_cidrs');

    const saved = await call('POST', ['ipacl'], { cidrs: ['10.0.0.0/8'] });
    assert.equal(saved.status, 200);

    const listed = await call('GET', ['ipacl']);
    assert.deepEqual((await listed.json()).data.cidrs, ['10.0.0.0/8']);

    // Remote client inside the range passes…
    const allowed = await call('GET', ['overview'], undefined, { 'x-forwarded-for': '10.9.9.9, 10.0.0.1' });
    assert.equal(allowed.status, 200);
    // …outside is blocked with ip_not_allowed…
    const denied = await call('GET', ['overview'], undefined, { 'x-forwarded-for': '192.168.5.5' });
    assert.equal(denied.status, 403);
    assert.equal((await denied.json()).error.code, 'ip_not_allowed');
    // …and mutations are equally covered.
    const deniedWrite = await call('DELETE', ['providers', '00000000-0000-0000-0000-000000000000'], undefined, { 'x-forwarded-for': '203.0.113.9' });
    assert.equal(deniedWrite.status, 403);

    // Direct access without XFF (local dashboard) bypasses by design.
    const local = await call('GET', ['overview']);
    assert.equal(local.status, 200);
  } finally {
    delete process.env[EDITION_ENV];
    await pool!.query(`DELETE FROM system_settings WHERE key='ipacl'`);
  }
});

test('ipacl enforcement stays dormant in OSS even when a list exists', options, async () => {
  await pool!.query(
    `INSERT INTO system_settings (key, value) VALUES ('ipacl', '{"cidrs":["10.0.0.0/8"]}'::jsonb)
     ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
  );
  delete process.env[EDITION_ENV];
  try {
    const res = await call('GET', ['overview'], undefined, { 'x-forwarded-for': '192.168.5.5' });
    assert.equal(res.status, 200, 'OSS must not enforce IP ACLs — enterprise features sleep by default');
  } finally {
    delete process.env[EDITION_ENV];
    await pool!.query(`DELETE FROM system_settings WHERE key='ipacl'`);
  }
});
