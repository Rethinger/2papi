import assert from 'node:assert/strict';
import test from 'node:test';

import { createCodexAccount } from '../lib/codex/create-account.ts';
import { codexCallbackCore, codexDeviceStartCore, codexDeviceStatusCore, codexImportAuthCore, codexOAuthStartCore, codexReauthorizeCore, codexRouteDeps, type CodexRouteDeps } from '../lib/codex/routes.ts';

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

function clientReturningAccount(revision = 2, calls: any[] = []) {
  return {
    query: async (sql: string, params?: any[]) => {
      calls.push({ sql, params });
      if (sql.includes('FROM providers')) return { rows: [{ id: 'provider-1' }] };
      if (sql.includes('RETURNING id, name, credential_revision')) return { rows: [{ id: 'acct-row', name: params?.[1], credential_revision: revision }] };
      return { rows: [] };
    },
  } as any;
}

const accountDeps = (calls: any[] = []) => ({
  insertSecret: async (_c: any, purpose: string, credential: any) => { calls.push({ helper: 'insertSecret', purpose, credential }); return 'sec-1'; },
  audit: async (_c: any, action: string, type: string, id?: string, payload?: any) => { calls.push({ helper: 'audit', action, type, id, payload }); },
  storeDraft: async () => { calls.push({ helper: 'storeDraft' }); return { version: 99 }; },
});

test('route deps default dashboard public URL is localhost:13000', () => {
  const old = process.env.DASHBOARD_PUBLIC_URL;
  delete process.env.DASHBOARD_PUBLIC_URL;
  try {
    assert.equal(codexRouteDeps().dashboardPublicUrl, 'http://localhost:13000');
  } finally {
    if (old === undefined) delete process.env.DASHBOARD_PUBLIC_URL;
    else process.env.DASHBOARD_PUBLIC_URL = old;
  }
});

test('OAuth start accepts dashboard localhost:13000 without callback Host 1455', async () => {
  const res = await codexOAuthStartCore(req('http://localhost:13000/api/control/v1/codex/oauth/start', { method: 'POST', headers: { host: 'localhost:13000' } }), deps());
  assert.equal(res.status, 200);
  assert.equal((await res.json()).authorization_url, 'https://auth.example/authorize');
});

test('OAuth start ignores spoofed forwarded host by default', async () => {
  const res = await codexOAuthStartCore(req('http://localhost:13000/api/control/v1/codex/oauth/start', { method: 'POST', headers: { host: 'localhost:13000', 'x-forwarded-host': 'evil.example' } }), deps());
  assert.equal(res.status, 200);
});

test('callback enforces loopback 1455 direct Host', async () => {
  const res = await codexCallbackCore(req('http://localhost:1455/auth/callback?code=c&state=good', { headers: { host: 'localhost:13000' } }), deps());
  assert.equal(res.status, 302);
  assert.equal(res.headers.get('location'), 'http://localhost:13000/?codex_status=invalid_host');
});

test('callback ignores spoofed X-Forwarded-Host unless TRUSTED_PROXY=true', async () => {
  const ok = await codexCallbackCore(req('http://localhost:1455/auth/callback?code=c&state=good', { headers: { host: 'localhost:1455', 'x-forwarded-host': 'evil.example' } }), deps());
  assert.equal(ok.headers.get('location'), 'http://localhost:13000/?codex_status=connected&account_id=account-1');

  const rejected = await codexCallbackCore(req('http://localhost:1455/auth/callback?code=c&state=good', { headers: { host: 'localhost:1455', 'x-forwarded-host': 'evil.example' } }), deps({ trustedProxy: true }));
  assert.equal(rejected.headers.get('location'), 'http://localhost:13000/?codex_status=invalid_host');
});

test('callback handles invalid/reused state and callback errors with safe redirect params', async () => {
  const invalid = await codexCallbackCore(req('http://localhost:1455/auth/callback?code=c&state=bad', { headers: { host: 'localhost:1455' } }), deps());
  assert.equal(invalid.status, 302);
  assert.equal(invalid.headers.get('location'), 'http://localhost:13000/?codex_status=invalid_state');

  const error = await codexCallbackCore(req('http://localhost:1455/auth/callback?error=access_denied&state=good&account_id=evil', { headers: { host: 'localhost:1455' } }), deps());
  assert.equal(error.headers.get('location'), 'http://localhost:13000/?codex_status=error');
});

test('provider error callback consumes OAuth state so replay fails', async () => {
  const states = new Set(['good']);
  const calls: string[] = [];
  const testDeps = deps({
    consumeOAuthSession: async state => {
      calls.push(state);
      if (!states.delete(state)) return null;
      return { state, nonce: 'n', verifier: 'v', created_at: new Date().toISOString() } as any;
    },
  });

  const first = await codexCallbackCore(req('http://localhost:1455/auth/callback?error=access_denied&state=good&account_id=evil', { headers: { host: 'localhost:1455' } }), testDeps);
  assert.equal(first.headers.get('location'), 'http://localhost:13000/?codex_status=error');

  const replay = await codexCallbackCore(req('http://localhost:1455/auth/callback?error=access_denied&state=good&account_id=evil', { headers: { host: 'localhost:1455' } }), testDeps);
  assert.equal(replay.headers.get('location'), 'http://localhost:13000/?codex_status=invalid_state');
  assert.deepEqual(calls, ['good', 'good']);
});

test('callback success redirects only codex_status and account ID to DASHBOARD_PUBLIC_URL', async () => {
  const res = await codexCallbackCore(req('http://localhost:1455/auth/callback?code=c&state=good&next=https://evil', { headers: { host: 'localhost:1455' } }), deps());
  assert.equal(res.status, 302);
  assert.equal(res.headers.get('location'), 'http://localhost:13000/?codex_status=connected&account_id=account-1');
});

test('import auth rejects chunked multibyte bodies larger than one MiB before parse', async () => {
  let parsed = false;
  const stream = new ReadableStream({
    start(controller) {
      const chunk = new TextEncoder().encode('é'.repeat(8 * 1024));
      for (let i = 0; i < 65; i += 1) controller.enqueue(chunk);
      controller.close();
    },
  });
  const res = await codexImportAuthCore(req('http://localhost:13000/api/control/v1/codex/import-auth', { method: 'POST', body: stream, duplex: 'half' } as any), deps({ parseCodexAuthFile: async () => { parsed = true; throw new Error('should_not_parse'); } }));
  assert.equal(res.status, 413);
  assert.equal(parsed, false);
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

test('createCodexAccount inserts Codex backend API base URL and returns credential revision', async () => {
  const calls: any[] = [];
  const result = await createCodexAccount(clientReturningAccount(3, calls), { name: 'codex-main', method: 'browser', credential: { kind: 'oauth', access_token: 'secret', chatgpt_account_id: 'verified', email: 'e@example.com', plan_type: 'plus' } as any }, accountDeps(calls));
  assert.deepEqual(result, { id: 'acct-row', revision: 3 });
  const insert = calls.find(c => c.sql?.includes('INSERT INTO accounts'));
  assert.match(insert.sql, /credential_revision/);
  assert.equal(insert.params[3], 'https://chatgpt.com/backend-api/codex');
  assert.equal(calls.find(c => c.helper === 'insertSecret').purpose, 'codex-oauth');
  assert.deepEqual(calls.find(c => c.helper === 'audit').payload, { account_id: 'acct-row', method: 'browser', plan: 'plus', revision: 3 });
  assert.ok(calls.some(c => c.helper === 'storeDraft'));
});

test('createCodexAccount defaults account name to email, account id, then generated safe name', async () => {
  const emailCalls: any[] = [];
  await createCodexAccount(clientReturningAccount(1, emailCalls), { method: 'import', credential: { kind: 'oauth', access_token: 's', chatgpt_account_id: 'acct ignored', email: 'User+Codex@Example.COM' } as any }, accountDeps(emailCalls));
  assert.equal(emailCalls.find(c => c.sql?.includes('INSERT INTO accounts')).params[1], 'codex-user-codex-example-com');

  const idCalls: any[] = [];
  await createCodexAccount(clientReturningAccount(1, idCalls), { method: 'import', credential: { kind: 'oauth', access_token: 's', chatgpt_account_id: 'Acct ID 42!' } as any }, accountDeps(idCalls));
  assert.equal(idCalls.find(c => c.sql?.includes('INSERT INTO accounts')).params[1], 'codex-acct-id-42');

  const generatedCalls: any[] = [];
  await createCodexAccount(clientReturningAccount(1, generatedCalls), { method: 'import', credential: { kind: 'oauth', access_token: 's', chatgpt_account_id: '' } as any }, accountDeps(generatedCalls));
  assert.match(generatedCalls.find(c => c.sql?.includes('INSERT INTO accounts')).params[1], /^codex-[a-f0-9]{12}$/);
});

test('createCodexAccount atomically increments credential revision on rotation/upsert', async () => {
  const insertCalls: any[] = [];
  await createCodexAccount(clientReturningAccount(4, insertCalls), { method: 'device', credential: { kind: 'oauth', access_token: 's', chatgpt_account_id: 'acct' } as any }, accountDeps(insertCalls));
  assert.match(insertCalls.find(c => c.sql?.includes('INSERT INTO accounts')).sql, /credential_revision=COALESCE\(accounts\.credential_revision, 0\)\+1/);

  const updateCalls: any[] = [];
  const result = await createCodexAccount(clientReturningAccount(5, updateCalls), { accountId: 'existing', method: 'browser', credential: { kind: 'oauth', access_token: 's', chatgpt_account_id: 'acct' } as any }, accountDeps(updateCalls));
  assert.equal(result.revision, 5);
  assert.match(updateCalls.find(c => c.sql?.includes('UPDATE accounts')).sql, /credential_revision=COALESCE\(credential_revision, 0\)\+1/);
});

test('createCodexAccount fails when provider row or required account update is missing', async () => {
  const providerMissing = { query: async () => ({ rows: [] }) } as any;
  await assert.rejects(() => createCodexAccount(providerMissing, { method: 'import', credential: { kind: 'oauth', access_token: 's', chatgpt_account_id: 'acct' } as any }, accountDeps([])), /codex_provider_missing/);

  const calls: any[] = [];
  const accountMissing = {
    query: async (sql: string, params?: any[]) => {
      calls.push({ sql, params });
      if (sql.includes('FROM providers')) return { rows: [{ id: 'provider-1' }] };
      if (sql.includes('RETURNING id, name, credential_revision')) return { rows: [] };
      return { rows: [] };
    },
  } as any;
  await assert.rejects(() => createCodexAccount(accountMissing, { accountId: 'missing', method: 'browser', credential: { kind: 'oauth', access_token: 's', chatgpt_account_id: 'acct' } as any }, accountDeps(calls)), /codex_account_missing/);
  assert.equal(calls.some(c => c.helper === 'storeDraft' || c.helper === 'audit'), false);
});
