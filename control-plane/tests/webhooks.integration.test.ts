import test from 'node:test';
import assert from 'node:assert/strict';
import crypto from 'node:crypto';
import fs from 'node:fs/promises';
import path from 'node:path';
import { Pool } from 'pg';
import { paddleWebhookCore } from '../lib/webhooks.ts';

// Money path: signature verification + idempotent credit. A replayed or
// forged webhook must never change a balance.

const url = process.env.TEST_DATABASE_URL;
const options = { skip: url ? false : 'TEST_DATABASE_URL is not set' };
if (url) process.env.DATABASE_URL = url;
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

const EDITION_ENV = '2PAPI_EDITION';
const SECRET = 'paddle-whsec-test';

function signedRequest(body: string, secret = SECRET) {
  const ts = Math.floor(Date.now() / 1000);
  const h1 = crypto.createHmac('sha256', secret).update(`${ts}:${body}`).digest('hex');
  return new Request('http://localhost/api/webhooks/paddle', {
    method: 'POST',
    headers: { 'content-type': 'application/json', 'paddle-signature': `ts=${ts};h1=${h1}` },
    body,
  });
}

function completedTx(teamId: string, totalCents = '500', txId = 'txn_001') {
  return JSON.stringify({
    event_type: 'transaction.completed',
    data: { id: txId, status: 'completed', currency_code: 'USD', total: totalCents, custom_data: { team_id: teamId } },
  });
}

const jsonOf = async (res: Response) => ({ status: res.status, body: await res.json() });

async function seedTeam(): Promise<string> {
  return (await pool!.query(`INSERT INTO teams (name, budget_usd, balance_usd) VALUES ('wh-' || gen_random_uuid()::text, 0, 0) RETURNING id`)).rows[0].id;
}

test('valid completed transaction credits the team exactly once', options, async () => {
  process.env[EDITION_ENV] = 'cloud';
  try {
    const teamId = await seedTeam();
    const res = await paddleWebhookCore(signedRequest(completedTx(teamId)), { pool: pool!, secret: SECRET });
    assert.equal(res.status, 200);
    assert.deepEqual((await res.json()).data, { ok: true, credited: true, delta_usd: 5 });

    const balance = Number((await pool!.query(`SELECT balance_usd FROM teams WHERE id=$1`, [teamId])).rows[0].balance_usd);
    assert.equal(balance, 5);

    // Replay: same transaction id → acknowledged, no double credit.
    const replay = await jsonOf(await paddleWebhookCore(signedRequest(completedTx(teamId)), { pool: pool!, secret: SECRET }));
    assert.equal(replay.body.data.credited, false);
    assert.equal(Number((await pool!.query(`SELECT balance_usd FROM teams WHERE id=$1`, [teamId])).rows[0].balance_usd), 5);

    // Different transaction id → credits again (legit new payment).
    const second = await jsonOf(await paddleWebhookCore(signedRequest(completedTx(teamId, '2000', 'txn_002')), { pool: pool!, secret: SECRET }));
    assert.equal(second.body.data.credited, true);
    assert.equal(Number((await pool!.query(`SELECT balance_usd FROM teams WHERE id=$1`, [teamId])).rows[0].balance_usd), 25);

    // Audit trail exists for operator tooling.
    const auditRows = (await pool!.query(`SELECT count(*)::int n FROM audit_events WHERE action='topup' AND resource_id=$1`, [teamId])).rows[0];
    assert.ok(auditRows.n >= 2);
  } finally {
    delete process.env[EDITION_ENV];
  }
});


test('forgeries and replays with stale timestamps are rejected', options, async () => {
  process.env[EDITION_ENV] = 'cloud';
  try {
    const teamId = await seedTeam();
    const body = completedTx(teamId);

    // Wrong secret.
    const forged = await paddleWebhookCore(signedRequest(body, 'attacker-secret'), { pool: pool!, secret: SECRET });
    assert.equal(forged.status, 401);

    // Stale timestamp (>5 min) even with the right secret.
    const oldTs = Math.floor(Date.now() / 1000) - 600;
    const staleMac = crypto.createHmac('sha256', SECRET).update(`${oldTs}:${body}`).digest('hex');
    const stale = await paddleWebhookCore(new Request('http://x', {
      method: 'POST',
      headers: { 'paddle-signature': `ts=${oldTs};h1=${staleMac}` },
      body,
    }), { pool: pool!, secret: SECRET });
    assert.equal(stale.status, 401);

    // Missing header.
    const noHeader = await paddleWebhookCore(new Request('http://x', { method: 'POST', headers: { 'content-type': 'application/json' }, body }), { pool: pool!, secret: SECRET });
    assert.equal(noHeader.status, 401);

    // Balances untouched.
    const n = Number((await pool!.query(`SELECT count(*)::int n FROM credit_transactions WHERE team_id=$1`, [teamId])).rows[0].n);
    assert.equal(n, 0);
  } finally {
    delete process.env[EDITION_ENV];
  }
});

test('non-USD, non-payable and anonymous checkouts are refused cleanly', options, async () => {
  process.env[EDITION_ENV] = 'cloud';
  try {
    const teamId = await seedTeam();
    const eur = JSON.stringify({ event_type: 'transaction.completed', data: { id: 'txn_eur', currency_code: 'EUR', total: '500', custom_data: { team_id: teamId } } });
    assert.equal((await jsonOf(await paddleWebhookCore(signedRequest(eur), { pool: pool!, secret: SECRET }))).status, 422);

    const pending = JSON.stringify({ event_type: 'transaction.completed', data: { id: 'txn_p', status: 'draft', currency_code: 'USD', total: '500', custom_data: { team_id: teamId } } });
    assert.equal((await jsonOf(await paddleWebhookCore(signedRequest(pending), { pool: pool!, secret: SECRET }))).status, 422);

    const noCustom = JSON.stringify({ event_type: 'transaction.completed', data: { id: 'txn_nc', currency_code: 'USD', total: '500' } });
    assert.equal((await jsonOf(await paddleWebhookCore(signedRequest(noCustom), { pool: pool!, secret: SECRET }))).status, 422);
  } finally {
    delete process.env[EDITION_ENV];
  }
});

test('webhooks are hosted-only and need configuration', options, async () => {
  delete process.env[EDITION_ENV];
  const gated = await paddleWebhookCore(signedRequest('{}'), { pool: pool!, secret: SECRET });
  assert.equal(gated.status, 403);

  process.env[EDITION_ENV] = 'cloud';
  try {
    const unconfigured = await paddleWebhookCore(signedRequest('{}'), { pool: pool! });
    assert.equal(unconfigured.status, 503);
    assert.equal((await unconfigured.json()).error.code, 'webhook_not_configured');
  } finally {
    delete process.env[EDITION_ENV];
  }
});
