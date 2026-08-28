import test from 'node:test';
import assert from 'node:assert/strict';
import crypto from 'node:crypto';
import { buildAuthUrl, discover, issueState, readState, verifyIdToken } from '../lib/oidc.ts';

const ISSUER = 'https://idp.test';
const discovery = {
  issuer: ISSUER,
  authorization_endpoint: 'https://idp.test/authorize',
  token_endpoint: 'https://idp.test/token',
  jwks_uri: 'https://idp.test/jwks',
};
const CLIENT_ID = '2papi-dashboard';
const CLIENT_SECRET = 'shared-secret-value';

function rsaKeypair() {
  const { publicKey, privateKey } = crypto.generateKeyPairSync('rsa', { modulusLength: 2048 });
  const jwk = publicKey.export({ format: 'jwk' }) as Record<string, string>;
  return { privateKey, jwk: { kty: jwk.kty!, n: jwk.n!, e: jwk.e!, alg: 'RS256', use: 'sig' } };
}

function b64json(obj: unknown) {
  return Buffer.from(JSON.stringify(obj)).toString('base64url');
}

function makeIdToken(keypair: { privateKey: crypto.KeyObject }, claims: Record<string, unknown>, alg = 'RS256', kid?: string) {
  const header = kid ? { alg, kid } : { alg };
  const input = `${b64json(header)}.${b64json(claims)}`;
  let sig: Buffer;
  if (alg === 'HS256') sig = crypto.createHmac('sha256', CLIENT_SECRET).update(input).digest();
  else if (alg === 'RS256') sig = crypto.sign('sha256', Buffer.from(input), keypair.privateKey);
  else throw new Error(`test alg ${alg} unsupported`);
  return `${input}.${sig.toString('base64url')}`;
}

const baseClaims = () => ({
  iss: ISSUER,
  aud: CLIENT_ID,
  sub: 'user-1',
  email: 'dev@corp.test',
  email_verified: true,
  exp: Math.floor(Date.now() / 1000) + 300,
});

test('state is signed, round-trips and rejects tampering/expiry', () => {
  const { state, nonce } = issueState();
  const read = readState(state);
  assert.ok(read);
  assert.equal(read!.nonce, nonce);

  const dot = state.lastIndexOf('.');
  assert.equal(readState(`${state.slice(0, dot)}.${'A'.repeat(state.length - dot - 1)}`), null, 'bad mac');
  assert.equal(readState(`${'x' + state.slice(0, dot)}${state.slice(dot)}`), null, 'tampered payload');

  const expired = new Date(Date.now() + 11 * 60_000); // clock moves past the 10-minute TTL
  const realNow = Date.now;
  Date.now = () => expired.getTime();
  try { assert.equal(readState(state), null, 'expired'); } finally { Date.now = realNow; }
});

test('discovery validates required endpoints', async () => {
  const fetchImpl = (async () => Response.json({
    issuer: ISSUER,
    authorization_endpoint: discovery.authorization_endpoint,
    token_endpoint: discovery.token_endpoint,
    jwks_uri: discovery.jwks_uri,
  })) as typeof fetch;
  assert.deepEqual(await discover(ISSUER, fetchImpl), discovery);

  const incomplete = (async () => Response.json({ issuer: ISSUER })) as typeof fetch;
  await assert.rejects(discover(ISSUER, incomplete), /missing_authorization_endpoint/);
});

test('buildAuthUrl carries required query parameters', () => {
  const url = new URL(buildAuthUrl(discovery, { issuer: ISSUER, client_id: CLIENT_ID, client_secret: 's', scopes: ['openid', 'email'] }, 'https://app.test/cb', 'st.ate'));
  assert.equal(url.origin + url.pathname, discovery.authorization_endpoint);
  assert.equal(url.searchParams.get('response_type'), 'code');
  assert.equal(url.searchParams.get('client_id'), CLIENT_ID);
  assert.equal(url.searchParams.get('redirect_uri'), 'https://app.test/cb');
  assert.equal(url.searchParams.get('scope'), 'openid email');
  assert.equal(url.searchParams.get('state'), 'st.ate');
});

test('verifyIdToken accepts a valid RS256 token via JWKS', async () => {
  const kp = rsaKeypair();
  const fetchImpl = (async () => Response.json({ keys: [kp.jwk] })) as typeof fetch;
  const claims = await verifyIdToken(makeIdToken(kp, baseClaims()), {
    discovery, clientId: CLIENT_ID, clientSecret: CLIENT_SECRET, nonce: undefined, fetchImpl,
  });
  assert.equal(claims.sub, 'user-1');
});

test('verifyIdToken enforces signature, expiry, iss, aud and nonce', async () => {
  const kp = rsaKeypair();
  const other = rsaKeypair();
  const fetchImpl = (async () => Response.json({ keys: [kp.jwk] })) as typeof fetch;
  const verify = (token: string, extra: Partial<{ nonce: string }> = {}) =>
    verifyIdToken(token, { discovery, clientId: CLIENT_ID, clientSecret: CLIENT_SECRET, fetchImpl, ...extra });

  await assert.rejects(verify(makeIdToken(other, baseClaims())), /bad_signature/);
  await assert.rejects(verify(makeIdToken(kp, { ...baseClaims(), exp: Math.floor(Date.now() / 1000) - 300 })), /expired/);
  await assert.rejects(verify(makeIdToken(kp, { ...baseClaims(), iss: 'https://evil.test' })), /iss_mismatch/);
  await assert.rejects(verify(makeIdToken(kp, { ...baseClaims(), aud: 'other-client' })), /aud_mismatch/);
  await assert.rejects(verify(makeIdToken(kp, baseClaims()), { nonce: 'expected-nonce' }), /nonce_mismatch/);
  await assert.rejects(verify(makeIdToken(kp, baseClaims()).replace(/^[^.]+\./, b64json({ alg: 'HS512' }) + '.')), /unsupported|signature/i);

  const okClaims = await verify(makeIdToken(kp, { ...baseClaims(), nonce: 'n-123' }), { nonce: 'n-123' });
  assert.equal(okClaims.email, 'dev@corp.test');
});

test('HS256 tokens validate against the client secret without JWKS', async () => {
  const kp = rsaKeypair(); // never used
  const emptyJwks = (async () => Response.json({ keys: [] })) as typeof fetch;
  const token = makeIdToken(kp, baseClaims(), 'HS256');
  const claims = await verifyIdToken(token, {
    discovery, clientId: CLIENT_ID, clientSecret: CLIENT_SECRET, fetchImpl: emptyJwks,
  });
  assert.equal(claims.email, 'dev@corp.test');

  await assert.rejects(
    verifyIdToken(token, { discovery, clientId: CLIENT_ID, clientSecret: 'wrong-secret', fetchImpl: emptyJwks }),
    /bad_signature/,
  );
});
