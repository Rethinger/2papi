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
  const token = signToken(privateKey, 'kid-1', { iss: process.env.CODEX_AUTH_ORIGIN, aud: 'app_EMoamEEZ73f0CkXaXp7hrann', exp: Math.floor(Date.now() / 1000) + 3600, nonce: 'nonce-1', sub: 'sub-1', chatgpt_account_id: 'acct-1', email: 'user@example.com', plan: 'plus' });
  const identity = await verifyOpenAIIDToken(token, 'nonce-1');
  assert.equal(identity.chatgpt_account_id, 'acct-1');
  assert.equal(identity.email, 'user@example.com');
  assert.equal(identity.plan_type, 'plus');
  await assert.rejects(() => verifyOpenAIIDToken(token, 'bad'), /nonce/);
  const tampered = token.replace(/.$/, token.endsWith('a') ? 'b' : 'a');
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

test('auth.json normalization is strict and refreshes expired imports once', async () => {
  let refreshes = 0;
  const server = await fakeAuthServer((req, res) => {
    if (req.url === '/oauth/token') {
      refreshes++;
      res.setHeader('content-type', 'application/json');
      res.end(JSON.stringify({ access_token: 'new-at', expires_in: 60 }));
    } else if (req.url === '/.well-known/jwks.json') res.end(JSON.stringify({ keys: [] }));
    else res.writeHead(404).end();
  });
  const { parseCodexAuthFile } = await import('../lib/codex/auth-file.ts');
  const current = await parseCodexAuthFile(JSON.stringify({ access_token: 'at', refresh_token: 'rt', expires_at: new Date(Date.now() + 60000).toISOString(), chatgpt_account_id: 'acct' }));
  assert.equal(current.access_token, 'at');
  await assert.rejects(() => parseCodexAuthFile('{'), /malformed/);
  await assert.rejects(() => parseCodexAuthFile(JSON.stringify({ OPENAI_API_KEY: 'sk' })), /api_key/);
  await assert.rejects(() => parseCodexAuthFile(JSON.stringify({ access_token: 'at', expires_at: '2000-01-01T00:00:00Z', chatgpt_account_id: 'acct' })), /expired_without_refresh/);
  await assert.rejects(() => parseCodexAuthFile(JSON.stringify({ access_token: 'at', expires_at: new Date(Date.now() + 60000).toISOString(), chatgpt_account_id: 'acct', extra: true })), /unknown_or_invalid/);
  const refreshed = await parseCodexAuthFile(JSON.stringify({ access_token: 'old', refresh_token: 'keep-rt', expires_at: '2000-01-01T00:00:00Z', chatgpt_account_id: 'acct' }));
  assert.equal(refreshed.access_token, 'new-at');
  assert.equal(refreshed.refresh_token, 'keep-rt');
  assert.equal(refreshes, 1);
  await server.close();
});

test('Device Code polling preserves pending state and authorizes once', async () => {
  let polls = 0;
  const server = await fakeAuthServer((req, res) => {
    res.setHeader('content-type', 'application/json');
    if (req.url === '/api/accounts/deviceauth/usercode') res.end(JSON.stringify({ device_code: 'dc', user_code: 'UC', expires_in: 900, interval: 1 }));
    else if (req.url === '/api/accounts/deviceauth/token' && ++polls === 1) res.writeHead(400).end(JSON.stringify({ error: 'authorization_pending' }));
    else if (req.url === '/api/accounts/deviceauth/token') res.end(JSON.stringify({ access_token: 'at', refresh_token: 'rt', expires_in: 60, account_id: 'acct' }));
    else res.writeHead(404).end();
  });
  const { startDeviceFlow, pollDeviceFlow } = await import('../lib/codex/device.ts');
  const flow = await startDeviceFlow();
  assert.equal((await pollDeviceFlow(flow.session)).state, 'pending');
  const done = await pollDeviceFlow(flow.session);
  assert.equal(done.state, 'authorized');
  assert.equal(done.credential?.chatgpt_account_id, 'acct');
  assert.equal((await pollDeviceFlow(flow.session)).state, 'expired');
  await server.close();
});

test('secret redaction recursively hides credential-bearing fields', async () => {
  const { redactCodexSecrets } = await import('../lib/codex/accounts.ts');
  assert.deepEqual(redactCodexSecrets({ access_token: 'at', nested: { refresh_token: 'rt', ok: true } }), { access_token: '[REDACTED]', nested: { refresh_token: '[REDACTED]', ok: true } });
});
