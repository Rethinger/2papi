process.env.CODEX_TEST_MODE = 'true';
process.env.REDIS_URL = 'redis://memory';

import test from 'node:test';
import assert from 'node:assert/strict';
import crypto from 'node:crypto';
import http from 'node:http';

function b64url(value: Buffer | string) { return Buffer.from(value).toString('base64url'); }

async function fakeAuthServer(handler: (req: http.IncomingMessage, res: http.ServerResponse) => void) {
  const server = http.createServer(handler);
  await new Promise<void>(resolve => server.listen(0, '127.0.0.1', resolve));
  const address = server.address();
  assert.ok(address && typeof address === 'object');
  process.env.CODEX_AUTH_ORIGIN = `http://127.0.0.1:${address.port}`;
  return { close: () => new Promise<void>(resolve => server.close(() => resolve())) };
}

function signToken(privateKey: crypto.KeyObject, kid: string, payload: Record<string, unknown>) {
  const header = b64url(JSON.stringify({ alg: 'RS256', typ: 'JWT', kid }));
  const body = b64url(JSON.stringify(payload));
  const sig = crypto.sign('RSA-SHA256', Buffer.from(`${header}.${body}`), privateKey).toString('base64url');
  return `${header}.${body}.${sig}`;
}

function authKeys() {
  const { publicKey, privateKey } = crypto.generateKeyPairSync('rsa', { modulusLength: 2048 });
  const jwk = publicKey.export({ format: 'jwk' }) as JsonWebKey;
  Object.assign(jwk, { kid: 'kid-1', alg: 'RS256', use: 'sig' });
  return { privateKey, jwk };
}

function idToken(privateKey: crypto.KeyObject, extra: Record<string, unknown> = {}) {
  return signToken(privateKey, 'kid-1', { iss: process.env.CODEX_AUTH_ORIGIN, aud: 'app_EMoamEEZ73f0CkXaXp7hrann', exp: Math.floor(Date.now() / 1000) + 3600, nonce: 'nonce-1', sub: 'sub-1', 'https://api.openai.com/auth': { chatgpt_account_id: 'acct', chatgpt_user_id: 'user-1', email: 'verified@example.com', chatgpt_plan_type: 'plus' }, ...extra });
}

test('JWT verification validates signature claims nonce and identity fields', async () => {
  const { publicKey, privateKey } = crypto.generateKeyPairSync('rsa', { modulusLength: 2048 });
  const jwk = publicKey.export({ format: 'jwk' }) as JsonWebKey;
  Object.assign(jwk, { kid: 'kid-1', alg: 'RS256', use: 'sig' });
  const server = await fakeAuthServer((req, res) => {
    if (req.url === '/.well-known/jwks.json') res.end(JSON.stringify({ keys: [jwk] }));
    else res.writeHead(404).end();
  });
  const { verifyOpenAIIDToken, clearJwksCacheForTests } = await import('../lib/codex/jwt.ts');
  clearJwksCacheForTests();
  const token = signToken(privateKey, 'kid-1', { iss: process.env.CODEX_AUTH_ORIGIN, aud: 'app_EMoamEEZ73f0CkXaXp7hrann', exp: Math.floor(Date.now() / 1000) + 3600, nonce: 'nonce-1', sub: 'sub-1', 'https://api.openai.com/auth': { chatgpt_account_id: 'acct-1', chatgpt_user_id: 'user-1', email: 'user@example.com', chatgpt_plan_type: 'plus' } });
  const identity = await verifyOpenAIIDToken(token, 'nonce-1');
  assert.equal(identity.chatgpt_account_id, 'acct-1');
  assert.equal(identity.email, 'user@example.com');
  assert.equal(identity.plan_type, 'plus');
  const structured = signToken(privateKey, 'kid-1', { iss: process.env.CODEX_AUTH_ORIGIN, aud: 'app_EMoamEEZ73f0CkXaXp7hrann', exp: Math.floor(Date.now() / 1000) + 3600, sub: 'sub-2', 'https://api.openai.com/auth': { chatgpt_account_id: 'acct-nested', chatgpt_plan_type: 'pro' } });
  assert.equal((await verifyOpenAIIDToken(structured)).chatgpt_account_id, 'acct-nested');
  await assert.rejects(() => verifyOpenAIIDToken(signToken(privateKey, 'kid-1', { iss: process.env.CODEX_AUTH_ORIGIN, aud: 'app_EMoamEEZ73f0CkXaXp7hrann', exp: Math.floor(Date.now() / 1000) + 3600, chatgpt_account_id: 'top-only', profile: { email: 'bad@example.com' }, plan: 'free' })), /openai_auth_claim/);
  await assert.rejects(() => verifyOpenAIIDToken(token, 'bad'), /nonce/);
  await assert.rejects(() => verifyOpenAIIDToken(signToken(privateKey, 'kid-1', { iss: process.env.CODEX_AUTH_ORIGIN, aud: 'app_EMoamEEZ73f0CkXaXp7hrann', exp: Math.floor(Date.now() / 1000) - 1, 'https://api.openai.com/auth': { chatgpt_account_id: 'acct' } })), /expired/);
  const parts = token.split('.');
  const tampered = `${parts[0]}.${parts[1]}.${Buffer.from('bad-signature').toString('base64url')}`;
  await assert.rejects(() => verifyOpenAIIDToken(tampered, 'nonce-1'), /signature/);
  await server.close();
});

test('OAuth sessions are one-time and do not expose verifier', async () => {
  const server = await fakeAuthServer((_req, res) => res.writeHead(404).end());
  const { startOAuthSession, consumeOAuthSession } = await import('../lib/codex/oauth.ts');
  const started = await startOAuthSession({ accountName: 'codex-main' }, 60);
  assert.match(started.authorizationUrl, /^http:\/\/127\.0\.0\.1:/);
  assert.ok(!JSON.stringify(started).includes('verifier'));
  const first = await consumeOAuthSession(started.state);
  const second = await consumeOAuthSession(started.state);
  assert.equal(first?.accountName, 'codex-main');
  assert.equal(second, null);
  await server.close();
});

test('browser OAuth requires verified ID-token identity and rejects body fallback', async () => {
  const { privateKey, jwk } = authKeys();
  let tokenBody: Record<string, unknown> = { access_token: 'at', id_token: idToken(privateKey, { nonce: '' }), expires_in: 60, account_id: 'body-only' };
  const server = await fakeAuthServer((req, res) => {
    res.setHeader('content-type', 'application/json');
    if (req.url === '/oauth/token') res.end(JSON.stringify(tokenBody));
    else if (req.url === '/.well-known/jwks.json') res.end(JSON.stringify({ keys: [jwk] }));
    else res.writeHead(404).end();
  });
  const { startOAuthSession, consumeOAuthSession, exchangeAuthorizationCode } = await import('../lib/codex/oauth.ts');
  const { clearJwksCacheForTests } = await import('../lib/codex/jwt.ts');
  clearJwksCacheForTests();
  const first = await startOAuthSession({}, 60);
  const firstSession = await consumeOAuthSession(first.state);
  assert.ok(firstSession);
  tokenBody = { access_token: 'at', expires_in: 60, account_id: 'body-only' };
  await assert.rejects(() => exchangeAuthorizationCode('code', firstSession), /missing_id_token/);
  const second = await startOAuthSession({}, 60);
  const secondSession = await consumeOAuthSession(second.state);
  assert.ok(secondSession);
  tokenBody = { access_token: 'at', id_token: idToken(privateKey, { nonce: secondSession.nonce, 'https://api.openai.com/auth': { chatgpt_account_id: 'verified-acct', email: 'verified@example.com', chatgpt_plan_type: 'plus' } }), expires_in: 60, account_id: 'body-only' };
  const credential = await exchangeAuthorizationCode('code', secondSession);
  assert.equal(credential.chatgpt_account_id, 'verified-acct');
  await server.close();
});

test('auth.json normalization is strict and refreshes expired imports once', async () => {
  let refreshes = 0;
  const { privateKey, jwk } = authKeys();
  const server = await fakeAuthServer((req, res) => {
    if (req.url === '/oauth/token') {
      refreshes++;
      res.setHeader('content-type', 'application/json');
      res.end(JSON.stringify({ access_token: 'new-at', id_token: idToken(privateKey), expires_in: 60 }));
    } else if (req.url === '/.well-known/jwks.json') res.end(JSON.stringify({ keys: [jwk] }));
    else res.writeHead(404).end();
  });
  const { clearJwksCacheForTests } = await import('../lib/codex/jwt.ts');
  clearJwksCacheForTests();
  const { parseCodexAuthFile } = await import('../lib/codex/auth-file.ts');
  const current = await parseCodexAuthFile(JSON.stringify({ OPENAI_API_KEY: null, last_refresh: new Date().toISOString(), tokens: { access_token: 'at', refresh_token: 'rt', id_token: idToken(privateKey), expires_at: new Date(Date.now() + 60000).toISOString() }, chatgpt_account_id: 'acct', email: 'unverified@example.com', plan_type: 'free' }));
  assert.equal(current.access_token, 'at');
  assert.equal(current.email, 'verified@example.com');
  assert.equal(current.plan_type, 'plus');
  await assert.rejects(() => parseCodexAuthFile('{'), /malformed/);
  await assert.rejects(() => parseCodexAuthFile(JSON.stringify({ OPENAI_API_KEY: 'sk' })), /api_key/);
  await assert.rejects(() => parseCodexAuthFile(JSON.stringify({ access_token: 'at', expires_at: '2000-01-01T00:00:00Z', chatgpt_account_id: 'acct' })), /expired_without_refresh|missing_verified_identity/);
  await assert.rejects(() => parseCodexAuthFile(JSON.stringify({ access_token: 'at', expires_at: new Date(Date.now() + 60000).toISOString(), chatgpt_account_id: 'acct', extra: true })), /unknown_or_invalid/);
  await assert.rejects(() => parseCodexAuthFile(JSON.stringify({ access_token: 'at', id_token: 'bad.token.value', expires_at: new Date(Date.now() + 60000).toISOString(), chatgpt_account_id: 'acct' })), /expired_without_refresh|id_token|JSON/);
  await assert.rejects(() => parseCodexAuthFile(JSON.stringify({ access_token: 'at', id_token: idToken(privateKey), expires_at: new Date(Date.now() + 60000).toISOString(), chatgpt_account_id: 'other' })), /mismatch/);
  const refreshed = await parseCodexAuthFile(JSON.stringify({ access_token: 'old', refresh_token: 'keep-rt', expires_at: '2000-01-01T00:00:00Z', chatgpt_account_id: 'acct' }));
  assert.equal(refreshed.access_token, 'new-at');
  assert.equal(refreshed.refresh_token, 'keep-rt');
  assert.equal(refreshes, 1);
  const expiredId = idToken(privateKey, { exp: Math.floor(Date.now() / 1000) - 1 });
  const futureAccessExpiredId = await parseCodexAuthFile(JSON.stringify({ access_token: 'old2', refresh_token: 'keep-rt2', id_token: expiredId, expires_at: new Date(Date.now() + 60000).toISOString(), chatgpt_account_id: 'acct', email: 'hint@example.com', plan_type: 'hint-plan' }));
  assert.equal(futureAccessExpiredId.access_token, 'new-at');
  assert.equal(futureAccessExpiredId.refresh_token, 'keep-rt2');
  assert.equal(futureAccessExpiredId.email, 'verified@example.com');
  assert.equal(futureAccessExpiredId.plan_type, 'plus');
  assert.equal(refreshes, 2);
  await server.close();
});

test('Device Code polling preserves pending state and authorizes once', async () => {
  let polls = 0;
  let deviceNonce = '';
  const { privateKey, jwk } = authKeys();
  const server = await fakeAuthServer(async (req, res) => {
    res.setHeader('content-type', 'application/json');
    if (req.url === '/api/accounts/deviceauth/usercode') {
      const chunks: Buffer[] = [];
      for await (const chunk of req) chunks.push(Buffer.from(chunk));
      deviceNonce = JSON.parse(Buffer.concat(chunks).toString()).nonce;
      res.end(JSON.stringify({ device_code: 'dc', user_code: 'UC', expires_in: 900, interval: 1 }));
    }
    else if (req.url === '/api/accounts/deviceauth/token' && ++polls === 1) res.writeHead(400).end(JSON.stringify({ error: 'authorization_pending' }));
    else if (req.url === '/api/accounts/deviceauth/token') res.end(JSON.stringify({ access_token: 'at', refresh_token: 'rt', id_token: idToken(privateKey, { nonce: deviceNonce }), expires_in: 60, account_id: 'body-must-not-win' }));
    else if (req.url === '/.well-known/jwks.json') res.end(JSON.stringify({ keys: [jwk] }));
    else res.writeHead(404).end();
  });
  const { clearJwksCacheForTests } = await import('../lib/codex/jwt.ts');
  clearJwksCacheForTests();
  const { startDeviceFlow, pollDeviceFlow } = await import('../lib/codex/device.ts');
  const flow = await startDeviceFlow();
  assert.ok(!('device_code' in flow));
  assert.equal((await pollDeviceFlow(flow.session)).state, 'pending');
  assert.equal((await pollDeviceFlow(flow.session)).state, 'slow_down');
  await new Promise(resolve => setTimeout(resolve, 1050));
  const concurrent = await Promise.all([pollDeviceFlow(flow.session), pollDeviceFlow(flow.session)]);
  const done = concurrent.find(result => result.state === 'complete');
  assert.ok(done);
  assert.equal(concurrent.filter(result => result.state === 'complete').length, 1);
  assert.equal(concurrent.filter(result => result.state === 'slow_down').length, 1);
  assert.equal(done.credential?.chatgpt_account_id, 'acct');
  assert.equal((await pollDeviceFlow(flow.session)).state, 'expired');
  await server.close();
});

test('Device Code preserves upstream expiry values and returns failed on network or invalid id token', async () => {
  let tokenMode: 'throw'|'invalid' = 'throw';
  const server = await fakeAuthServer((req, res) => {
    res.setHeader('content-type', 'application/json');
    if (req.url === '/api/accounts/deviceauth/usercode') res.end(JSON.stringify({ device_code: 'dc', user_code: 'UC', expires_in: 1234, interval: 0 }));
    else if (req.url === '/api/accounts/deviceauth/token' && tokenMode === 'throw') req.socket.destroy();
    else if (req.url === '/api/accounts/deviceauth/token') res.end(JSON.stringify({ access_token: 'at', id_token: 'bad.token.value', expires_in: 60 }));
    else if (req.url === '/.well-known/jwks.json') res.end(JSON.stringify({ keys: [] }));
    else res.writeHead(404).end();
  });
  const { startDeviceFlow, pollDeviceFlow } = await import('../lib/codex/device.ts');
  const flow = await startDeviceFlow(900);
  assert.equal(flow.expires_in, 1234);
  assert.equal(flow.interval, 0);
  assert.equal((await pollDeviceFlow(flow.session)).state, 'failed');
  tokenMode = 'invalid';
  assert.equal((await pollDeviceFlow(flow.session)).state, 'failed');
  await server.close();
});

test('Device Code pending response never extends the original upstream deadline', async () => {
  const server = await fakeAuthServer(async (req, res) => {
    res.setHeader('content-type', 'application/json');
    if (req.url === '/api/accounts/deviceauth/usercode') res.end(JSON.stringify({ device_code: 'dc-expiry', user_code: 'UC', expires_in: 0.05, interval: 0 }));
    else if (req.url === '/api/accounts/deviceauth/token') {
      await new Promise(resolve => setTimeout(resolve, 75));
      res.writeHead(400).end(JSON.stringify({ error: 'authorization_pending' }));
    } else res.writeHead(404).end();
  });
  const { startDeviceFlow, pollDeviceFlow } = await import('../lib/codex/device.ts');
  const flow = await startDeviceFlow();
  assert.equal((await pollDeviceFlow(flow.session)).state, 'pending');
  assert.equal((await pollDeviceFlow(flow.session)).state, 'expired');
  await server.close();
});

test('secret redaction recursively hides credential-bearing fields', async () => {
  const { redactCodexSecrets } = await import('../lib/codex/accounts.ts');
  assert.deepEqual(redactCodexSecrets({ access_token: 'at', nested: { refresh_token: 'rt', ok: true } }), { access_token: '[REDACTED]', nested: { refresh_token: '[REDACTED]', ok: true } });
});
