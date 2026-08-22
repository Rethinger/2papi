import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import { Pool } from 'pg';
import { signupCore, verifyCore, loginCore, logoutCore, meCore } from '../lib/cloud-auth.ts';
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

test('me/logout close the loop over sessions', options, async () => {
  process.env[EDITION_ENV] = 'cloud';
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
