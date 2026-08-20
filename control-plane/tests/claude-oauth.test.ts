import test from 'node:test';
import assert from 'node:assert/strict';
import { claudeCallbackCore, claudeOAuthStartCore, type ClaudeRouteDeps } from '../lib/claude/routes.ts';
import { CLAUDE_SCOPE, claudeAuthorizeUrl, exchangeClaudeCode, type ClaudeOAuthSession } from '../lib/claude/oauth.ts';

function routeDeps(overrides: Partial<ClaudeRouteDeps> = {}): ClaudeRouteDeps {
  return {
    dashboardPublicUrl: 'http://localhost:13000',
    trustedProxy: false,
    startOAuthSession: async () => ({ authorization_url: claudeAuthorizeUrl('s', 'challenge'), state: 's', expires_in: 600 }),
    consumeOAuthSession: async (state: string) => state === 's' ? ({ state: 's', verifier: 'v', created_at: 'now' } satisfies ClaudeOAuthSession) : null,
    exchangeAuthorizationCode: async () => ({ access_token: 'sk-ant-oauth', refresh_token: 'rt', expires_at: '2027-01-01T00:00:00Z', email: 'user@example.com', account_uuid: 'acct-1' }),
    createAccount: async () => ({ id: 'account-1', revision: 1 }),
    ...overrides,
  };
}

test('authorization URL carries client id, PKCE and unencoded scope colons', () => {
  const url = claudeAuthorizeUrl('state-1', 'challenge-1');
  assert.ok(url.startsWith('https://claude.ai/oauth/authorize'));
  assert.match(url, /client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e/);
  assert.match(url, /code_challenge=challenge-1/);
  assert.match(url, /code_challenge_method=S256/);
  assert.ok(url.includes('redirect_uri=' + encodeURIComponent('http://localhost:54545/callback')));
  assert.ok(url.includes('code=true'));
  for (const scope of CLAUDE_SCOPE.split(' ')) {
    assert.ok(url.includes(encodeURIComponent(scope).replace(/%3A/gi, ':')), `missing scope ${scope}`);
  }
  const scopeParam = url.split('scope=')[1] ?? '';
  assert.ok(!scopeParam.includes('%3A'), 'scope colons must stay unencoded');
});

test('oauth start returns the authorization URL from the deps', async () => {
  const deps = routeDeps();
  const result = await claudeOAuthStartCore(new Request('http://localhost:13000/api/control/v1/claude/oauth/start', { method: 'POST', body: '{}' }), deps);
  const body = await result.json() as { authorization_url: string };
  assert.ok(body.authorization_url.startsWith('https://claude.ai/oauth/authorize'));
});

test('callback on the loopback host exchanges the code and redirects to the dashboard', async () => {
  const deps = routeDeps();
  const request = new Request('http://localhost:54545/callback?code=c1&state=s');
  const response = await claudeCallbackCore(request, deps);
  assert.equal(response.status, 302);
  const location = response.headers.get('location');
  assert.ok(location?.includes('claude_status=connected'));
  assert.ok(location?.includes('account_id=account-1'));
});

test('callback rejects non-loopback hosts', async () => {
  const deps = routeDeps();
  const request = new Request('http://claude.example.com/callback?code=c1&state=s');
  const response = await claudeCallbackCore(request, deps);
  assert.equal(response.status, 403);
});

test('callback redirects to error for unknown state', async () => {
  const deps = routeDeps();
  const request = new Request('http://localhost:54545/callback?code=c1&state=unknown');
  const response = await claudeCallbackCore(request, deps);
  assert.equal(response.status, 302);
  assert.match(response.headers.get('location') ?? '', /claude_status=error/);
});

test('exchangeClaudeCode posts JSON to the token endpoint and parses tokens', async () => {
  const originalFetch = globalThis.fetch;
  let capturedBody: unknown = null;
  globalThis.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
    capturedBody = init?.body;
    return new Response(JSON.stringify({
      access_token: 'sk-ant-new',
      refresh_token: 'rt-new',
      expires_in: 3600,
      account: { email_address: 'a@b.c', uuid: 'acct-9' },
    }), { status: 200, headers: { 'Content-Type': 'application/json' } });
  };
  try {
    const token = await exchangeClaudeCode('code-x', 'state-s', { state: 'state-s', verifier: 'verifier-v', created_at: 'now' });
    assert.equal(token.access_token, 'sk-ant-new');
    assert.equal(token.refresh_token, 'rt-new');
    assert.equal(token.email, 'a@b.c');
    assert.equal(token.account_uuid, 'acct-9');
    const body = JSON.parse(String(capturedBody)) as Record<string, unknown>;
    assert.equal(body.grant_type, 'authorization_code');
    assert.equal(body.code_verifier, 'verifier-v');
    assert.equal(body.client_id, '9d1c250a-e61b-44d9-88ed-5944d1962f5e');
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('exchangeClaudeCode fails on missing access token', async () => {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => new Response(JSON.stringify({}), { status: 200, headers: { 'Content-Type': 'application/json' } });
  try {
    await assert.rejects(() => exchangeClaudeCode('code-x', 'state-s', { state: 'state-s', verifier: 'v', created_at: 'now' }), /missing_access_token/);
  } finally {
    globalThis.fetch = originalFetch;
  }
});
