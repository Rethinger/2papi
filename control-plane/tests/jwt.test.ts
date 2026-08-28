import test from 'node:test';
import assert from 'node:assert/strict';
import crypto from 'node:crypto';
import { signJwt, verifyJwt } from '../lib/jwt.ts';

const SECRET = 'unit-test-secret';
const base = { sub: 'user-1', role: 'operator' };

test('roundtrip: sign then verify returns claims', () => {
  const token = signJwt(base, SECRET, 300);
  const claims = verifyJwt(token, SECRET);
  assert.ok(claims);
  assert.equal(claims!.sub, 'user-1');
  assert.equal(claims!.role, 'operator');
  assert.equal(claims!.aud, '2papi-api');
});

test('tampered payload and wrong secret are rejected', () => {
  const token = signJwt(base, SECRET, 300);
  const [h, , sig] = token.split('.');
  const forgedPayload = Buffer.from(JSON.stringify({ ...base, exp: Math.floor(Date.now() / 1000) + 9999, aud: '2papi-api', iat: 1 })).toString('base64url');
  assert.equal(verifyJwt(`${h}.${forgedPayload}.${sig}`, SECRET), null, 'signature must not survive payload swap');
  assert.equal(verifyJwt(token, 'other-secret'), null);
});

test('expired tokens rejected; grace window is 30s', () => {
  // exp 50s in the past — beyond the 30s skew.
  const expired = signJwt(base, SECRET, 10, Date.now() - 60_000);
  assert.equal(verifyJwt(expired, SECRET), null);

  // Issued 40s ago with ttl 20s → exp ≈ now-20s → inside the 30s grace → accepted.
  const withinGrace = signJwt(base, SECRET, 20, Date.now() - 40_000);
  assert.ok(verifyJwt(withinGrace, SECRET) !== null);
});

test('alg none and HS512 confusion are rejected', () => {
  const noneToken = `${Buffer.from('{"alg":"none","typ":"JWT"}').toString('base64url')}.${Buffer.from(JSON.stringify({ sub: 'x', aud: '2papi-api', exp: 9999999999 })).toString('base64url')}.`;
  assert.equal(verifyJwt(noneToken, SECRET), null);

  const input = `${Buffer.from('{"alg":"HS512","typ":"JWT"}').toString('base64url')}.${Buffer.from(JSON.stringify({ sub: 'x', role: 'operator', aud: '2papi-api', exp: 9999999999 })).toString('base64url')}`;
  const hs512 = `${input}.${crypto.createHmac('sha512', SECRET).update(input).digest('base64url')}`;
  assert.equal(verifyJwt(hs512, SECRET), null, 'only HS256 is accepted');
});
