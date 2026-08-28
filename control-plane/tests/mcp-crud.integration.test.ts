import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import { Pool } from 'pg';
import { compileDeclarativeSnapshot, materializeRuntimeSnapshot } from '../lib/snapshots.ts';

// MCP servers CRUD (dashboard path). Credentials ride encrypted in
// secret_records: declarative snapshots stay credential-free, only the
// runtime materialization carries decrypted headers.

const url = process.env.TEST_DATABASE_URL;
const options = { skip: url ? false : 'TEST_DATABASE_URL is not set' };
if (url) process.env.DATABASE_URL = url;
const pool = url ? new Pool({ connectionString: url, max: 2 }) : null;

let route: any;

test.before(async () => {
  if (!pool) return;
  await pool.query('DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;');
  const dir = path.join(process.cwd(), 'migrations');
  for (const name of (await fs.readdir(dir)).filter(name => name.endsWith('.sql')).sort()) {
    await pool.query(await fs.readFile(path.join(dir, name), 'utf8'));
  }
  await pool.query(
    `INSERT INTO virtual_keys (name, key_hash, key_prefix, models, rpm) VALUES ('seed-key', $1, 'sk-seed', ARRAY['model-a'], 60)`,
    ['a'.repeat(64)],
  );
  route = await import('../app/api/control/v1/[[...resource]]/route.ts');
});

test.after(async () => {
  await pool?.end();
});

const call = async (method: 'GET' | 'POST' | 'PATCH' | 'DELETE', resource: string[], body?: unknown, id?: string) => {
  const p = id ? [...resource, id] : resource;
  const req = new Request(`http://localhost/api/control/v1/${p.join('/')}`, {
    method,
    ...(body === undefined ? {} : { body: JSON.stringify(body), headers: { 'content-type': 'application/json' } }),
  });
  return route[method](req, { params: Promise.resolve({ resource: p }) });
};

const SECRET_HEADERS = { Authorization: 'Bearer mcp-upstream-secret' };

test('mcp servers CRUD keeps credentials out of declarative snapshots', options, async () => {
  const created = await (await call('POST', ['mcp-servers'], {
    name: 'tools',
    url: 'https://mcp.internal/mcp',
    headers: SECRET_HEADERS,
  })).json();
  assert.ok(created.data.id, 'created');

  // Duplicate name rejected.
  const dup = await call('POST', ['mcp-servers'], { name: 'TOOLS', url: 'https://x.test' });
  assert.equal(dup.status, 409);

  // Listing never returns header values.
  const list = await (await call('GET', ['mcp-servers'])).json();
  assert.equal(list.data.length, 1);
  assert.equal(list.data[0].headers_set, true);
  assert.ok(!JSON.stringify(list).includes('mcp-upstream-secret'), 'secret must not leak through GET');

  // Declarative snapshot is credential-free but carries the server…
  let client = await pool!.connect();
  let declarativeBody: string;
  let runtime: any;
  try {
    const compiled = await compileDeclarativeSnapshot(client);
    declarativeBody = JSON.stringify(compiled.snapshot);
    runtime = await materializeRuntimeSnapshot(client, compiled.snapshot);
  } finally {
    client.release();
  }
  const declared = JSON.parse(declarativeBody).mcp_servers;
  assert.deepEqual(declared, [{ name: 'tools', url: 'https://mcp.internal/mcp', enabled: true }]);
  assert.ok(!declarativeBody.includes('mcp-upstream-secret'), 'stored snapshot stays credential-free');

  // …while the runtime snapshot carries decrypted headers.
  assert.deepEqual(runtime.mcp_servers, [
    { name: 'tools', url: 'https://mcp.internal/mcp', headers: SECRET_HEADERS },
  ]);

  // Rotation via PATCH.
  const rotated = { Authorization: 'Bearer rotated-secret' };
  await call('PATCH', ['mcp-servers'], { headers: rotated }, String(created.data.id));
  client = await pool!.connect();
  try {
    runtime = await materializeRuntimeSnapshot(client, (await compileDeclarativeSnapshot(client)).snapshot);
  } finally {
    client.release();
  }
  assert.deepEqual(runtime.mcp_servers[0].headers, rotated);

  // DELETE removes the row.
  await call('DELETE', ['mcp-servers'], undefined, String(created.data.id));
  const after = await (await call('GET', ['mcp-servers'])).json();
  assert.equal(after.data.length, 0);

  // And with no enabled servers the snapshot drops the field entirely.
  client = await pool!.connect();
  try {
    const compiled = await compileDeclarativeSnapshot(client);
    assert.equal(compiled.snapshot.mcp_servers, undefined);
  } finally {
    client.release();
  }
});
