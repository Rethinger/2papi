import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import { Pool } from 'pg';
import { compileDeclarativeSnapshot } from '../lib/snapshots.ts';

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
  // Any config mutation publishes a draft snapshot, which refuses to compile
  // without at least one enabled key — seed one so CRUD routes work.
  await pool.query(
    `INSERT INTO virtual_keys (name, key_hash, key_prefix, models, rpm)
     VALUES ('seed-key', $1, 'sk-seed', ARRAY['model-a'], 60)`,
    ['a'.repeat(64)],
  );
  // Hosted editions require an operator session on control mutations.
  await pool.query(
    `INSERT INTO users (email, password_hash, platform_role) VALUES ('op@test.local', 'x', 'operator')`,
  );
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

// Operator session cookie minted once per run (see before()).
let operatorCookie = '';

function call(method: 'GET' | 'POST' | 'PATCH' | 'DELETE', resource: string[], body?: unknown, id?: string) {
  const path = id ? [...resource, id] : resource;
  const req = new Request(`http://localhost/api/control/v1/${path.join('/')}`, {
    method,
    headers: { 'content-type': 'application/json', cookie: operatorCookie },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  });
  const ctx = { params: Promise.resolve({ resource: path }) };
  if (method === 'GET') return route.GET(req, ctx);
  if (method === 'POST') return route.POST(req, ctx);
  if (method === 'PATCH') return route.PATCH(req, ctx);
  return route.DELETE(req, ctx);
}

const dataOf = async (res: Response) => {
  const j = await res.json();
  if (!res.ok || j.error) console.error(`[api ${res.status}]`, JSON.stringify(j).slice(0, 400));
  return j.data;
};
const errorOf = async (res: Response) => (await res.json()).error;

async function seedKeyAndTeam(name: string) {
  const team = (await pool!.query(`INSERT INTO teams (name, budget_usd) VALUES ($1, 0) RETURNING id`, [`${name}-team`])).rows[0];
  await pool!.query(
    `INSERT INTO virtual_keys (name, key_hash, key_prefix, models, rpm, team_id)
     VALUES ($1, $2, $3, ARRAY['model-a'], 60, $4)`,
    [name, 'b'.repeat(64), `sk-${name}`, team.id],
  );
  return team;
}

test('organizations API is gated by the orgs feature', options, async () => {
  delete process.env[EDITION_ENV];
  const res = await call('POST', ['organizations'], { name: 'acme' });
  assert.equal(res.status, 403);
  assert.equal((await errorOf(res)).code, 'feature_not_licensed');

  // Reads sleep too.
  const list = await call('GET', ['organizations']);
  assert.equal(list.status, 403);

  // Teams CRUD stays OSS even with the gate closed.
  const team = (await pool!.query(`INSERT INTO teams (name, budget_usd) VALUES ('oss-team', 5) RETURNING id`)).rows[0];
  const patched = await call('PATCH', ['teams'], { budget_usd: 7 }, String(team.id));
  assert.equal(patched.status, 200);
  assert.equal(Number((await dataOf(patched)).budget_usd), 7);
});

test('organization CRUD + team binding flows into the snapshot', options, async () => {
  process.env[EDITION_ENV] = 'ent';
  try {
    const org = await dataOf(await call('POST', ['organizations'], { name: 'Acme', budget_usd: 100 }));
    assert.ok(org.id);
    assert.equal(Number(org.budget_usd), 100);

    // Duplicate names are rejected case-insensitively (015 unique index).
    const dup = await call('POST', ['organizations'], { name: 'ACME', budget_usd: 1 });
    assert.equal(dup.status, 409);
    assert.equal((await errorOf(dup)).code, 'organization_exists');

    await seedKeyAndTeam('acme-key');
    const teamRow = (await pool!.query(`SELECT id FROM teams WHERE name='acme-key-team'`)).rows[0];
    const bound = await call('PATCH', ['teams'], { org_id: org.id }, String(teamRow.id));
    assert.equal(bound.status, 200);

    let client = await pool!.connect();
    let snapKey: any;
    try {
      snapKey = (await compileDeclarativeSnapshot(client)).snapshot.virtual_keys.find((k: any) => k.name === 'acme-key');
    } finally { client.release(); }
    assert.deepEqual(snapKey.team.org, { id: org.id, budget_usd: 100 }, 'org budget rides on the team payload');

    // Org budget update propagates; GET computes remaining headroom.
    await call('PATCH', ['organizations'], { budget_usd: 50 }, String(org.id));
    client = await pool!.connect();
    try {
      snapKey = (await compileDeclarativeSnapshot(client)).snapshot.virtual_keys.find((k: any) => k.name === 'acme-key');
    } finally { client.release(); }
    assert.deepEqual(snapKey.team.org, { id: org.id, budget_usd: 50 });

    const listed = await dataOf(await call('GET', ['organizations']));
    const row = listed.find((o: any) => o.id === org.id);
    assert.equal(row.team_count, 1);
    assert.equal(Number(row.team_budget_sum), 0);
    assert.equal(Number(row.budget_remaining_usd), 50);

    // Delete unbinds teams via FK ON DELETE SET NULL.
    const del = await call('DELETE', ['organizations'], undefined, String(org.id));
    assert.equal(del.status, 200);
    const after = (await pool!.query(`SELECT org_id FROM teams WHERE id=$1`, [teamRow.id])).rows[0];
    assert.equal(after.org_id, null);

    client = await pool!.connect();
    try {
      snapKey = (await compileDeclarativeSnapshot(client)).snapshot.virtual_keys.find((k: any) => k.name === 'acme-key');
    } finally { client.release(); }
    // Team budget is 0 and the org is gone -> the whole team payload drops out.
    assert.ok(!snapKey.team || !snapKey.team.org, 'org disappears from snapshot after delete');
  } finally {
    delete process.env[EDITION_ENV];
  }
});

test('org cap reaches keys of unlimited-budget teams and org_admin roles are valid', options, async () => {
  process.env[EDITION_ENV] = 'ent';
  try {
    const org = await dataOf(await call('POST', ['organizations'], { name: 'CapCo', budget_usd: 30 }));
    // Team budget 0 (= unlimited): the org cap still applies upstream.
    const team = await seedKeyAndTeam('capco-key');
    await call('PATCH', ['teams'], { org_id: org.id }, String(team.id));

    const user = (await pool!.query(
      `INSERT INTO users (email, password_hash) VALUES ('admin@capco.test', 'x') RETURNING id`,
    )).rows[0];
    // Migration 015 extended the role CHECK: org_admin/team_admin accepted.
    await pool!.query(
      `INSERT INTO team_members (team_id, user_id, role) VALUES ($1,$2,'org_admin')`,
      [team.id, user.id],
    );

    const client = await pool!.connect();
    let snapKey: any;
    try {
      snapKey = (await compileDeclarativeSnapshot(client)).snapshot.virtual_keys.find((k: any) => k.name === 'capco-key');
    } finally { client.release(); }
    assert.equal(Number(snapKey.team.budget_usd), 0);
    assert.deepEqual(snapKey.team.org, { id: org.id, budget_usd: 30 });
  } finally {
    delete process.env[EDITION_ENV];
  }
});
