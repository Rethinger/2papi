import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import { Pool } from 'pg';
import { signupCore, verifyCore, loginCore, logoutCore, meCore } from '../lib/cloud-auth.ts';
import { clearRateLimitsForTests } from '../lib/rate-limit.ts';
import { resolveSession } from '../lib/auth.ts';

// Шаг 6 хребта (Cloud): self-serve signup → verification (bonus + team +
// first key) → login/logout/me. Hosted editions only.

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

const post = (pathName: string, body?: unknown, cookie?: string) =>
  new Request(`http://localhost:3000${pathName}`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', ...(cookie ? { cookie } : {}) },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  });

const jsonOf = async (res: Response) => ({ status: res.status, body: await res.json(), headers: res.headers });

test('self-serve flows are closed in plain OSS', options, async () => {
  delete process.env[EDITION_ENV];
  for (const [core, body] of [[signupCore, { email: 'a@b.test', password: 'longenough123' }], [loginCore, { email: 'a@b.test', password: 'x' }]] as const) {
    const res = await core(post('/api/auth/x', body));
    assert.equal(res.status, 403);
    assert.equal((await res.json()).error.code, 'hosted_only');
  }
});

test('signup → verify provisions team, key and the credit grant', options, async () => {
  process.env[EDITION_ENV] = 'cloud';
  try {
    const email = 'dev@example.com';
    const signupRes = await jsonOf(await signupCore(post('/api/auth/signup', { email, password: 'longenough123' })));
    assert.equal(signupRes.status, 200);

    // Duplicate signup is a silent no-op (no enumeration).
    const dupRes = await jsonOf(await signupCore(post('/api/auth/signup', { email, password: 'other-password-1' })));
    assert.equal(dupRes.status, 200);

    // Login before verification is refused.
    const early = await jsonOf(await loginCore(post('/api/auth/login', { email, password: 'longenough123' })));
    assert.equal(early.status, 403);
    assert.equal(early.body.error.code, 'email_unverified');

    // Weak passwords never reach the database.
    const weak = await jsonOf(await signupCore(post('/api/auth/signup', { email: 'weak@example.com', password: 'short' })));
    assert.equal(weak.status, 400);

    // Email delivery is operator wiring; the test mints a known token the
    // same way an SMTP worker would have received one. Stored value is a
    // hash, so a leaked DB row is not a usable link.
    const crypto = await import('node:crypto');
    process.env.SIGNUP_BONUS_USD = '2';
    const plaintextToken = crypto.randomBytes(32).toString('base64url');
    const knownHash = crypto.createHash('sha256').update(plaintextToken).digest('hex');
    await pool!.query(`DELETE FROM email_verification_tokens WHERE user_id=(SELECT id FROM users WHERE lower(email)=$1)`, [email]);
    await pool!.query(
      `INSERT INTO email_verification_tokens (user_id, token_hash, expires_at)
       VALUES ((SELECT id FROM users WHERE lower(email)=$1), $2, now() + interval '24 hours')`,
      [email, knownHash],
    );

    const verified = await jsonOf(await verifyCore(post('/api/auth/verify', { token: plaintextToken })));
    assert.equal(verified.status, 200);

    const team = (await pool!.query(
      `SELECT t.* FROM teams t JOIN team_members tm ON tm.team_id=t.id
       JOIN users u ON u.id=tm.user_id WHERE lower(u.email)=$1 AND tm.role='owner'`,
      [email],
    )).rows[0];
    assert.ok(team, 'personal team provisioned');
    assert.equal(Number(team.balance_usd), 2, 'signup bonus credited to balance');

    const ledger = (await pool!.query(`SELECT * FROM credit_transactions WHERE team_id=$1`, [team.id])).rows;
    assert.equal(ledger.length, 1);
    assert.equal(ledger[0].source, 'signup_bonus');
    assert.equal(Number(ledger[0].delta_usd), 2);

    const key = (await pool!.query(`SELECT * FROM virtual_keys WHERE team_id=$1`, [team.id])).rows[0];
    assert.ok(key && key.enabled, 'first virtual key issued');

    // Second verification with any token for this user fails cleanly.
    const again = await jsonOf(await verifyCore(post('/api/auth/verify', { token: plaintextToken })));
    assert.equal(again.status, 400);
    assert.equal((await pool!.query(`SELECT count(*)::int n FROM credit_transactions WHERE team_id=$1`, [team.id])).rows[0].n, 1, 'grant is idempotent');

    // Login works now and issues a live session.
    const login = await loginCore(post('/api/auth/login', { email, password: 'longenough123' }));
    assert.equal(login.status, 200);
    const cookie = /papi_session=([^;]+)/.exec(login.headers.get('set-cookie')!)![1];
    const me = await resolveSession(pool!, cookie);
    assert.equal(me!.email.toLowerCase(), email);

    // Wrong password and unknown user are indistinguishable.
    const wrongPw = await jsonOf(await loginCore(post('/api/auth/login', { email, password: 'wrong-password-1' })));
    const unknown = await jsonOf(await loginCore(post('/api/auth/login', { email: 'ghost@example.com', password: 'wrong-password-1' })));
    assert.equal(wrongPw.status, 401);
    assert.deepEqual(wrongPw.body.error, unknown.body.error);
  } finally {
    delete process.env[EDITION_ENV];
    delete process.env.SIGNUP_BONUS_USD;
  }
});

test('me/logout close the loop over sessions', options, async () => {  process.env[EDITION_ENV] = 'cloud';
  try {
    const me = await meCore(new Request('http://x/api/auth/session'));
    assert.equal(me.status, 401);

    const login = await loginCore(post('/api/auth/login', { email: 'dev@example.com', password: 'longenough123' }));
    const cookie = login.headers.get('set-cookie')!;
    const sessionReq = new Request('http://x/api/auth/session', { headers: { cookie } });
    const profile = await jsonOf(await meCore(sessionReq));
    assert.equal(profile.status, 200);
    assert.ok(profile.body.data.team, 'team attached to profile');
    assert.equal(Number(profile.body.data.team.balance_usd), 2);

    const out = await logoutCore(post('/api/auth/session', undefined, cookie));
    assert.equal(out.status, 200);
    assert.equal((await jsonOf(await meCore(sessionReq))).status, 401, 'session revoked server-side');
  } finally {
    delete process.env[EDITION_ENV];
  }
});


test('prepaid balance decrements on spend and reconciles from the ledger', options, async () => {  process.env[EDITION_ENV] = 'cloud';
  try {
    const { storeRequestEvents } = await import('../lib/request-events.ts');
    const { reconcileTeamBalances } = await import('../lib/balance.ts');

    const teamId = (await pool!.query(
      `SELECT t.id FROM teams t JOIN team_members tm ON tm.team_id=t.id
       JOIN users u ON u.id=tm.user_id WHERE lower(u.email)='dev@example.com'`,
    )).rows[0].id;
    const keyRow = (await pool!.query(`SELECT id, name FROM virtual_keys WHERE team_id=$1 LIMIT 1`, [teamId])).rows[0];

    // Spend 0.5 through the normal ingest path → balance 2 − 0.5.
    await storeRequestEvents(pool!, 'gw-bal', [{
      request_id: 'b'.repeat(32) + '1',
      occurred_at: new Date().toISOString(),
      endpoint: '/v1/chat/completions',
      public_model: 'model-a',
      upstream_model: 'up-a',
      virtual_key: keyRow.name,
      virtual_key_id: keyRow.id,
      streaming: false,
      config_version: 1,
      final_status: 200,
      success: true,
      total_latency_ms: 40,
      input_tokens: 10,
      output_tokens: 5,
      total_tokens: 15,
      cost_usd: 0.5,
      attempts: [],
    }]);
    let balance = Number((await pool!.query(`SELECT balance_usd FROM teams WHERE id=$1`, [teamId])).rows[0].balance_usd);
    assert.equal(balance, 1.5, 'ingest decrements the prepaid balance in-transaction');

    // Corrupt the live value; reconcile restores it from ledger − spend.
    await pool!.query(`UPDATE teams SET balance_usd=99 WHERE id=$1`, [teamId]);
    const report = await reconcileTeamBalances(pool!);
    assert.ok(report.updated >= 1);
    balance = Number((await pool!.query(`SELECT balance_usd FROM teams WHERE id=$1`, [teamId])).rows[0].balance_usd);
    assert.equal(balance, 1.5, 'reconcile computes grants − successful spend');

    // Unsuccessful requests never cost anything.
    await storeRequestEvents(pool!, 'gw-bal', [{
      request_id: 'b'.repeat(31) + 'c2',
      occurred_at: new Date().toISOString(),
      endpoint: '/v1/chat/completions',
      public_model: 'model-a',
      upstream_model: 'up-a',
      virtual_key: keyRow.name,
      virtual_key_id: keyRow.id,
      streaming: false,
      config_version: 1,
      final_status: 500,
      success: false,
      total_latency_ms: 12,
      input_tokens: 100,
      output_tokens: 0,
      total_tokens: 100,
      cost_usd: 9.99,
      attempts: [],
    }]);
    balance = Number((await pool!.query(`SELECT balance_usd FROM teams WHERE id=$1`, [teamId])).rows[0].balance_usd);
    assert.equal(balance, 1.5, 'failed requests do not drain the balance');
  } finally {
    delete process.env[EDITION_ENV];
  }
});


test('operator manual adjustment lands in ledger, balance and audit', options, async () => {
  process.env[EDITION_ENV] = 'cloud';
  try {
    const route: any = await import('../app/api/control/v1/[[...resource]]/route.ts');
    const { createSession } = await import('../lib/auth.ts');
    const teamId = (await pool!.query(
      `SELECT t.id FROM teams t JOIN team_members tm ON tm.team_id=t.id
       JOIN users u ON u.id=tm.user_id WHERE lower(u.email)='dev@example.com'`,
    )).rows[0].id;
    // Promote the tenant to operator for this test and mint their session
    // (hosted editions gate control mutations behind operator sessions).
    const uid = (await pool!.query(`SELECT id FROM users WHERE lower(email)='dev@example.com'`)).rows[0].id;
    await pool!.query(`UPDATE users SET platform_role='operator' WHERE id=$1`, [uid]);
    const opSession = await createSession(pool!, uid);
    const before = Number((await pool!.query(`SELECT balance_usd FROM teams WHERE id=$1`, [teamId])).rows[0].balance_usd);

    const req = new Request('http://x/api/control/v1/billing/adjust', {
      method: 'POST',
      headers: { 'content-type': 'application/json', authorization: 'Bearer operator', cookie: `papi_session=${opSession.token}` },
      body: JSON.stringify({ team_id: teamId, delta_usd: -0.25, note: 'compensation for outage' }),
    });
    const res = await route.POST(req, { params: Promise.resolve({ resource: ['billing', 'adjust'] }) });
    assert.equal(res.status, 200);
    const after = Number((await pool!.query(`SELECT balance_usd FROM teams WHERE id=$1`, [teamId])).rows[0].balance_usd);
    assert.equal(Math.round((after - before) * 1e6) / 1e6, -0.25);

    const txRow = (await pool!.query(
      `SELECT kind, source, delta_usd FROM credit_transactions WHERE team_id=$1 AND source='manual' ORDER BY created_at DESC LIMIT 1`,
      [teamId],
    )).rows[0];
    assert.equal(txRow.kind, 'adjustment');
    assert.equal(Number(txRow.delta_usd), -0.25);

    // Validation: zero amount refused.
    const zero = await route.POST(new Request('http://x/api/control/v1/billing/adjust', {
      method: 'POST', headers: { 'content-type': 'application/json', cookie: `papi_session=${opSession.token}` },
      body: JSON.stringify({ team_id: teamId, delta_usd: 0 }),
    }), { params: Promise.resolve({ resource: ['billing', 'adjust'] }) });
    assert.equal(zero.status, 422);
  } finally {
    delete process.env[EDITION_ENV];
  }
});


test('tenant billing endpoint shows own team only', options, async () => {
  process.env[EDITION_ENV] = 'cloud';
  try {
    const { billingCore } = await import('../lib/cloud-auth.ts');
    const login = await loginCore(post('/api/auth/login', { email: 'dev@example.com', password: 'longenough123' }));
    const cookie = login.headers.get('set-cookie')!;

    // Other teams exist in the DB but must not leak.
    await pool!.query(`INSERT INTO teams (name, budget_usd, balance_usd) VALUES ('stranger-team', 0, 999)`);
    const strangerKey = (await pool!.query(
      `INSERT INTO credit_transactions (team_id, delta_usd, kind, source, external_id)
       SELECT id, 777,'topup','paddle','txn_stranger' FROM teams WHERE name='stranger-team' RETURNING id`,
    )).rows[0];

    const req = new Request('http://x/api/auth/billing', { headers: { cookie: `papi_session=${cookie.match(/papi_session=([^;]+)/)![1]}` } });
    void strangerKey;
    const res = await billingCore(req);
    assert.equal(res.status, 200);
    const data = (await res.json()).data;
    assert.equal(data.team.name.startsWith('dev@'), true, 'own personal team by email');
    assert.ok(Number(data.balance_usd) > 0 && Number(data.balance_usd) < 900, 'own balance only');
    for (const txRow of data.transactions) {
      assert.equal(txRow.source === 'paddle' && Number(txRow.delta_usd) === 777, false, 'no foreign transactions');
    }
    assert.ok(Array.isArray(data.keys) && data.keys.length >= 1, 'own keys listed');

    // No session → 401.
    const anon = await billingCore(new Request('http://x/api/auth/billing'));
    assert.equal(anon.status, 401);
  } finally {
    delete process.env[EDITION_ENV];
  }
});


test('signup and login are rate limited per ip', options, async () => {
  process.env[EDITION_ENV] = 'cloud';
  process.env.SIGNUP_RATE_LIMIT = '2';
  process.env.LOGIN_RATE_LIMIT = '2';
  try {
    const ip = (n: number) => `10.9.9.${n}`;
    const emailAt = (n: number) => `ratelimit${n}@example.com`;
    const reqFromIp = (p: string, b: unknown, n: number) =>
      new Request(`http://localhost:3000${p}`, {
        method: 'POST',
        headers: { 'content-type': 'application/json', 'x-forwarded-for': ip(n) },
        body: JSON.stringify(b),
      });

    // Signup: two attempts from one ip pass, third is throttled.
    assert.equal((await signupCore(reqFromIp('/api/auth/signup', { email: emailAt(1), password: 'longenough123' }, 1))).status, 200);
    assert.equal((await signupCore(reqFromIp('/api/auth/signup', { email: emailAt(2), password: 'longenough123' }, 1))).status, 200);
    const third = await signupCore(reqFromIp('/api/auth/signup', { email: emailAt(3), password: 'longenough123' }, 1));
    assert.equal(third.status, 429);
    assert.equal((await third.json()).error.code, 'rate_limited');

    // Another ip is unaffected.
    assert.equal((await signupCore(reqFromIp('/api/auth/signup', { email: emailAt(3), password: 'longenough123' }, 2))).status, 200);

    // Login counts against its own bucket (unverified still consumes a slot).
    const loginFrom = (n: number, email: string) => loginCore(reqFromIp('/api/auth/login', { email, password: 'longenough123' }, n));
    await loginFrom(3, emailAt(1));
    await loginFrom(3, emailAt(1));
    const throttled = await loginFrom(3, emailAt(1));
    assert.equal(throttled.status, 429);
    assert.equal((await throttled.json()).error.code, 'rate_limited');
  } finally {
    delete process.env[EDITION_ENV];
    delete process.env.SIGNUP_RATE_LIMIT;
    delete process.env.LOGIN_RATE_LIMIT;
    clearRateLimitsForTests();
  }
});
