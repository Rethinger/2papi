import test from 'node:test';
import assert from 'node:assert/strict';
import crypto from 'node:crypto';
import fs from 'node:fs/promises';
import path from 'node:path';
import { Pool } from 'pg';
import { oidcCallbackCore, oidcStartCore, getOidcConfig } from '../lib/sso-routes.ts';
import { resolveSession } from '../lib/auth.ts';

// Full OIDC round-trip against an in-process fake IdP (mocked fetch), plus
// operator config gating through the catch-all control route.

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
  // Operator session for gated control mutations (see requireOperator).
  await pool.query(
    `INSERT INTO users (email, password_hash, platform_role) VALUES ('op@sso.test', 'x', 'operator')`,
  );
  route = await import('../app/api/control/v1/[[...resource]]/route.ts');
  const { createSession } = await import('../lib/auth.ts');
  const opUser = (await pool.query(`SELECT id FROM users WHERE email='op@sso.test'`)).rows[0];
  const session = await createSession(pool, opUser.id);
  operatorCookie = `papi_session=${session.token}`;
});

test.after(async () => {
  await pool?.end();
});

const EDITION_ENV = '2PAPI_EDITION';
let operatorCookie = '';

const ISSUER = 'https://idp.test';
const CLIENT_ID = '2papi-dashboard';
const CLIENT_SECRET = 'idp-client-secret';
const discoveryBody = {
  issuer: ISSUER,
  authorization_endpoint: 'https://idp.test/authorize',
  token_endpoint: 'https://idp.test/token',
  jwks_uri: 'https://idp.test/jwks',
};

function rsaKeypair() {
  const { publicKey, privateKey } = crypto.generateKeyPairSync('rsa', { modulusLength: 2048 });
  const jwk = publicKey.export({ format: 'jwk' }) as Record<string, string>;
  return { privateKey, jwk: { kty: jwk.kty!, n: jwk.n!, e: jwk.e!, alg: 'RS256', use: 'sig' } };
}
const kp = rsaKeypair();

function idToken(claims: Record<string, unknown>) {
  const input = `${Buffer.from(JSON.stringify({ alg: 'RS256', kid: 'k1' })).toString('base64url')}.${Buffer.from(JSON.stringify(claims)).toString('base64url')}`;
  const sig = crypto.sign('sha256', Buffer.from(input), kp.privateKey);
  return `${input}.${sig.toString('base64url')}`;
}

function fakeIdPFetch(tokenResponses: Array<{ status?: number; body?: unknown }> = [], seen: Request[] = []) {
  let tokenCall = 0;
  return async (input: string | Request, init?: RequestInit): Promise<Response> => {
    const href = typeof input === 'string' ? input : input.url;
    seen.push(new Request(href, init));
    if (href.endsWith('/openid-configuration')) return Response.json(discoveryBody);
    if (href.endsWith('/jwks')) return Response.json({ keys: [kp.jwk] });
    if (href.endsWith('/token')) {
      const resp = tokenResponses[tokenCall++] ?? {};
      return Response.json(resp.body ?? {}, { status: resp.status ?? 200 });
    }
    throw new Error(`unexpected fetch ${href}`);
  };
}

const callControl = (method: 'GET' | 'POST', resource: string[], body?: unknown) =>
  route[method](
    new Request(`http://localhost/api/control/v1/${resource.join('/')}`, {
      method,
      headers: { 'content-type': 'application/json', cookie: operatorCookie },
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    }),
    { params: Promise.resolve({ resource }) },
  );

test('operator config is gated by the sso feature', options, async () => {
  delete process.env[EDITION_ENV];
  const post = await callControl('POST', ['oidc'], { issuer: ISSUER, client_id: CLIENT_ID, client_secret: 'x' });
  assert.equal(post.status, 403);
  const get = await callControl('GET', ['oidc']);
  assert.equal(get.status, 403);
});

test('operator config saves and reads back without the secret', options, async () => {
  process.env[EDITION_ENV] = 'ent';
  try {
    const saved = await (await callControl('POST', ['oidc'], { issuer: ISSUER, client_id: CLIENT_ID, client_secret: CLIENT_SECRET })).json();
    assert.equal(saved.data.enabled, true);
    assert.equal(saved.data.issuer, ISSUER);
    assert.equal(saved.data.client_id, CLIENT_ID);
    assert.ok(!JSON.stringify(saved).includes(CLIENT_SECRET), 'secret never leaves the server');

    const stored = await getOidcConfig(pool!);
    assert.equal(stored!.client_secret, CLIENT_SECRET);

    // Validation: issuer must be a URL.
    const bad = await callControl('POST', ['oidc'], { issuer: 'not-a-url', client_id: 'a', client_secret: 'b' });
    assert.equal(bad.status, 400);
  } finally {
    delete process.env[EDITION_ENV];
  }
});

async function runStartCallback(userClaims: Record<string, unknown>, opts: {
  tokenResponse?: { status?: number; body?: unknown };
  tamperState?: boolean;
} = {}) {
  process.env[EDITION_ENV] = 'ent';
  try {
    const startRes = await oidcStartCore(
      new Request('http://localhost:3000/api/auth/oidc/start'),
      { pool: pool!, fetchImpl: fakeIdPFetch(), originOverride: 'http://dash.test' },
    );
    assert.equal(startRes.status, 302);
    const location = new URL(startRes.headers.get('location')!);
    const state = location.searchParams.get('state');
    const cookie = startRes.headers.get('set-cookie')!;
    const stateFromCookie = /papi_oidc_state=([^;]+)/.exec(cookie)![1];
    assert.equal(location.origin + location.pathname, 'https://idp.test/authorize');
    assert.equal(state, stateFromCookie, 'URL state equals cookie state');

    const nonce = JSON.parse(Buffer.from(state.split('.')[0], 'base64url').toString()).n as string;

    const claims = { iss: ISSUER, aud: CLIENT_ID, sub: 'sub-1', email: 'dev@corp.test', email_verified: true, exp: Math.floor(Date.now() / 1000) + 300, nonce, ...userClaims };
    const callbackUrl = new URL('https://dash.test/api/auth/oidc/callback');
    callbackUrl.searchParams.set('code', 'auth-code-1');
    // Forged flow: query state is bogus while the browser cookie keeps the
    // real one — the mismatch branch must fire before any IdP call.
    callbackUrl.searchParams.set('state', opts.tamperState ? 'forged.state' : state!);
    const callbackRes = await oidcCallbackCore(
      new Request(callbackUrl.toString(), { headers: { cookie: `papi_oidc_state=${stateFromCookie}` } }),
      { pool: pool!, fetchImpl: fakeIdPFetch([opts.tokenResponse ?? { body: { id_token: idToken(claims) } }]), originOverride: 'http://dash.test' },
    );
    return { callbackRes, nonce };
  } finally {
    delete process.env[EDITION_ENV];
  }
}

test('happy path: code exchange provisions user and issues a session', options, async () => {
  const { callbackRes } = await runStartCallback({});
  assert.equal(callbackRes.status, 302);
  assert.equal(callbackRes.headers.get('location'), 'http://dash.test/');
  const setCookie = callbackRes.headers.get('set-cookie')!;
  assert.match(setCookie, /papi_session=/);
  assert.match(setCookie, /HttpOnly/);

  const user = (await pool!.query(`SELECT * FROM users WHERE lower(email)='dev@corp.test'`)).rows[0];
  assert.ok(user, 'user provisioned');
  assert.ok(user.email_verified_at, 'IdP-verified email marked verified');

  const sessionRow = (await pool!.query('SELECT * FROM user_sessions WHERE user_id=$1', [user.id])).rows[0];
  assert.ok(sessionRow, 'session persisted');
});

test('callback rejects forged state before touching the IdP', options, async () => {
  const { callbackRes } = await runStartCallback({}, { tamperState: true });
  assert.equal(callbackRes.status, 401);
  const body = await callbackRes.json();
  assert.equal(body.error.code, 'sso_state_mismatch');
});

test('callback surfaces token endpoint failures', options, async () => {
  const { callbackRes } = await runStartCallback({}, { tokenResponse: { status: 400, body: { error: 'bad_grant' } } });
  assert.equal(callbackRes.status, 401);
  assert.equal((await callbackRes.json()).error.code, 'sso_token_exchange_failed');
});

test('unverified emails are refused', options, async () => {
  const { callbackRes } = await runStartCallback({ email_verified: false });
  assert.equal(callbackRes.status, 403);
  assert.equal((await callbackRes.json()).error.code, 'sso_email_unverified');
});

test('disabled accounts cannot log in through SSO', options, async () => {
  await pool!.query(`UPDATE users SET disabled_at=now() WHERE lower(email)='dev@corp.test'`);
  try {
    const { callbackRes } = await runStartCallback({});
    assert.equal(callbackRes.status, 403);
    assert.equal((await callbackRes.json()).error.code, 'sso_user_disabled');
  } finally {
    await pool!.query(`UPDATE users SET disabled_at=NULL WHERE lower(email)='dev@corp.test'`);
  }
});

test('issued sessions resolve to the user and expire cleanly', options, async () => {
  const { callbackRes } = await runStartCallback({ email: 'second@corp.test' });
  const token = /papi_session=([^;\n]+)/.exec(callbackRes.headers.get('set-cookie')!)![1];
  const resolved = await resolveSession(pool!, token);
  assert.equal(resolved!.email, 'second@corp.test');
  assert.equal(await resolveSession(pool!, 'garbage-token'), null);
});
