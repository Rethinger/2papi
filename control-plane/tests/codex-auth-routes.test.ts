import assert from 'node:assert/strict';
import test from 'node:test';

import { createCodexAccount } from '../lib/codex/create-account.ts';
import { codexCallbackCore, codexDeviceStartCore, codexDeviceStatusCore, codexImportAuthCore, codexOAuthStartCore, codexReauthorizeCore, type CodexRouteDeps } from '../lib/codex/routes.ts';

function req(url: string, init: RequestInit & { headers?: Record<string, string> } = {}) {
  return new Request(url, init);
}

function deps(overrides: Partial<CodexRouteDeps> = {}): CodexRouteDeps {
  const created: any[] = [];
  return {
    dashboardPublicUrl: 'http://localhost:13000',
    trustedProxy: false,
    startOAuthSession: async () => ({ state: 'state-1', authorizationUrl: 'https://auth.example/authorize', expires_in: 600 }),
    consumeOAuthSession: async state => state === 'good' ? { state, nonce: 'n', verifier: 'v', created_at: new Date().toISOString() } as any : null,
    exchangeAuthorizationCode: async () => ({ kind: 'oauth', access_token: 'at', chatgpt_account_id: 'acct-1', auth_method: 'browser' } as any),
    parseCodexAuthFile: async () => ({ kind: 'oauth', access_token: 'at', chatgpt_account_id: 'acct-import', auth_method: 'import' } as any),
    startDeviceFlow: async () => ({ session: 'sess', user_code: 'UC', verification_uri: 'https://verify', expires_in: 900, interval: 5 }),
    pollDeviceFlow: async () => ({ state: 'pending' as const, interval: 5 }),
    createAccount: async input => { created.push(input); return { id: 'account-1', revision: 7 }; },
    now: () => new Date('2026-08-05T00:00:00Z'),
    ...overrides,
  };
}

test('OAuth start accepts direct Host localhost:1455 and ignores spoofed forwarded host by default', async () => {
  const res = await codexOAuthStartCore(req('http://localhost:1455/api/control/v1/codex/oauth/start', { method: 'POST', headers: { host: 'localhost:1455', 'x-forwarded-host': 'evil.example' } }), deps());
  assert.equal(res.status, 200);
  assert.equal((await res.json()).authorization_url, 'https://auth.example/authorize');
});

test('OAuth start rejects spoofed X-Forwarded-Host when TRUSTED_PROXY=true', async () => {
  const res = await codexOAuthStartCore(req('http://localhost:1455/api/control/v1/codex/oauth/start', { method: 'POST', headers: { host: 'localhost:1455', 'x-forwarded-host': 'evil.example' } }), deps({ trustedProxy: true }));
  assert.equal(res.status, 400);
});

test('callback handles invalid/reused state and callback errors with safe redirect params', async () => {
  const invalid = await codexCallbackCore(req('http://localhost:1455/auth/callback?code=c&state=bad', { headers: { host: 'localhost:1455' } }), deps());
  assert.equal(invalid.status, 302);
  assert.equal(invalid.headers.get('location'), 'http://localhost:13000/?codex_status=invalid_state');

  const error = await codexCallbackCore(req('http://localhost:1455/auth/callback?error=access_denied&state=good&account_id=evil', { headers: { host: 'localhost:1455' } }), deps());
  assert.equal(error.headers.get('location'), 'http://localhost:13000/?codex_status=error');
});

test('callback success redirects only codex_status and account ID to DASHBOARD_PUBLIC_URL', async () => {
  const res = await codexCallbackCore(req('http://localhost:1455/auth/callback?code=c&state=good&next=https://evil', { headers: { host: 'localhost:1455' } }), deps());
  assert.equal(res.status, 302);
  assert.equal(res.headers.get('location'), 'http://localhost:13000/?codex_status=connected&account_id=account-1');
});

test('import auth rejects bodies larger than one MiB with 413', async () => {
  const res = await codexImportAuthCore(req('http://localhost:13000/api/control/v1/codex/import-auth', { method: 'POST', body: 'x'.repeat(1024 * 1024 + 1) }), deps());
  assert.equal(res.status, 413);
});

test('device start and status expose state transitions and create account on completion', async () => {
  const start = await codexDeviceStartCore(req('http://localhost:13000/api/control/v1/codex/device/start', { method: 'POST' }), deps());
  assert.equal(start.status, 200);
  assert.deepEqual(await start.json(), { session: 'sess', user_code: 'UC', verification_uri: 'https://verify', expires_in: 900, interval: 5 });

  const pending = await codexDeviceStatusCore(req('http://localhost:13000/api/control/v1/codex/device/sess/status'), 'sess', deps({ pollDeviceFlow: async () => ({ state: 'pending', interval: 5 }) }));
  assert.deepEqual(await pending.json(), { state: 'pending', interval: 5 });

  const complete = await codexDeviceStatusCore(req('http://localhost:13000/api/control/v1/codex/device/sess/status'), 'sess', deps({ pollDeviceFlow: async () => ({ state: 'complete', credential: { kind: 'oauth', access_token: 'at', chatgpt_account_id: 'acct-device', auth_method: 'device' } as any }) }));
  assert.deepEqual(await complete.json(), { state: 'complete', account_id: 'account-1' });
});

test('reauthorize starts OAuth session for a fixed existing account', async () => {
  const res = await codexReauthorizeCore(req('http://localhost:13000/api/control/v1/accounts/a1/reauthorize', { method: 'POST' }), 'a1', deps({ startOAuthSession: async options => ({ state: String(options?.accountId), authorizationUrl: 'https://auth.example/reauth', expires_in: 600 }) }));
  assert.deepEqual(await res.json(), { authorization_url: 'https://auth.example/reauth', expires_in: 600 });
});

test('createCodexAccount inserts or rotates encrypted credential, persists profile, safe audit, and compiles draft', async () => {
  const calls: any[] = [];
  const client = { query: async (sql: string, params: any[]) => { calls.push({ sql, params }); if (sql.includes('INSERT INTO accounts')) return { rows: [{ id: 'acct-row', name: params[1] }] }; if (sql.includes('RETURNING id')) return { rows: [{ id: 'sec-1' }] }; return { rows: [] }; } } as any;
  const result = await createCodexAccount(client, { name: 'codex-main', method: 'browser', credential: { kind: 'oauth', access_token: 'secret', chatgpt_account_id: 'verified', email: 'e@example.com', plan_type: 'plus' } as any }, { insertSecret: async (_c, purpose, credential) => { calls.push({ helper: 'insertSecret', purpose, credential }); return 'sec-1'; }, audit: async (_c, action, type, id, payload) => { calls.push({ helper: 'audit', action, type, id, payload }); }, storeDraft: async () => { calls.push({ helper: 'storeDraft' }); return { version: 9 }; } });
  assert.deepEqual(result, { id: 'acct-row', revision: 9 });
  assert.equal(calls.find(c => c.helper === 'insertSecret').purpose, 'codex-oauth');
  assert.match(calls.find(c => c.sql?.includes('INSERT INTO accounts')).sql, /external_account_id/);
  assert.deepEqual(calls.find(c => c.helper === 'audit').payload, { account_id: 'acct-row', method: 'browser', plan: 'plus', revision: 9 });
  assert.ok(calls.some(c => c.helper === 'storeDraft'));
});
