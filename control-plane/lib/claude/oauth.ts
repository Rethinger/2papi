import crypto from 'node:crypto';
import { consumeJsonOnce, setJsonWithTtl } from '../redis';

// claude.ai OAuth client registration used by the official Claude Code CLI
// (browser login for Claude Pro/Max subscriptions). The resulting OAuth token
// authenticates against api.anthropic.com with Authorization: Bearer.
export const CLAUDE_CLIENT_ID = '9d1c250a-e61b-44d9-88ed-5944d1962f5e';
export const CLAUDE_AUTHORIZE_URL = 'https://claude.ai/oauth/authorize';
export const CLAUDE_TOKEN_URL = 'https://api.anthropic.com/v1/oauth/token';
export const CLAUDE_REDIRECT_URI = 'http://localhost:54545/callback';
// Anthropic's OAuth server expects colons inside scope values unencoded.
export const CLAUDE_SCOPE = 'org:create_api_key user:profile user:inference';

export type ClaudeOAuthSession = { state: string; verifier: string; created_at: string };
export type ClaudeTokenData = {
  access_token: string;
  refresh_token?: string;
  expires_at: string;
  email?: string;
  account_uuid?: string;
};

function b64url(buf: Buffer) { return buf.toString('base64url'); }
function sessionKey(state: string) { return `claude:oauth:${state}`; }

// claudeAuthorizeUrl builds the claude.ai OAuth authorization URL. Scope
// colons are deliberately left unencoded, matching Anthropic's OAuth server.
export function claudeAuthorizeUrl(state: string, codeChallenge: string): string {
  const params = new URLSearchParams({
    code: 'true',
    client_id: CLAUDE_CLIENT_ID,
    response_type: 'code',
    redirect_uri: CLAUDE_REDIRECT_URI,
    code_challenge: codeChallenge,
    code_challenge_method: 'S256',
    state,
  });
  const scopeEncoded = CLAUDE_SCOPE.split(' ')
    .map(value => encodeURIComponent(value).replace(/%3A/gi, ':'))
    .join('+');
  return `${CLAUDE_AUTHORIZE_URL}?${params.toString()}&scope=${scopeEncoded}`;
}

export async function startClaudeOAuthSession(ttlSeconds = 600): Promise<{ authorization_url: string; state: string; expires_in: number }> {
  const state = b64url(crypto.randomBytes(32));
  const verifier = b64url(crypto.randomBytes(32));
  const challenge = b64url(crypto.createHash('sha256').update(verifier).digest());
  await setJsonWithTtl(sessionKey(state), { state, verifier, created_at: new Date().toISOString() } satisfies ClaudeOAuthSession, ttlSeconds);
  return { authorization_url: claudeAuthorizeUrl(state, challenge), state, expires_in: ttlSeconds };
}

export async function consumeClaudeOAuthSession(state: string) {
  return consumeJsonOnce<ClaudeOAuthSession>(sessionKey(state));
}

export async function exchangeClaudeCode(code: string, state: string, session: ClaudeOAuthSession): Promise<ClaudeTokenData> {
  const response = await fetch(CLAUDE_TOKEN_URL, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      code,
      grant_type: 'authorization_code',
      client_id: CLAUDE_CLIENT_ID,
      redirect_uri: CLAUDE_REDIRECT_URI,
      code_verifier: session.verifier,
      state,
    }),
  });
  if (!response.ok) {
    throw new Error(`claude_token_exchange_failed_${response.status}`);
  }
  const data = await response.json() as Record<string, unknown>;
  if (typeof data.access_token !== 'string' || !data.access_token) throw new Error('claude_missing_access_token');
  const expiresIn = typeof data.expires_in === 'number' ? data.expires_in : 3600;
  const account = data.account && typeof data.account === 'object' ? data.account as Record<string, unknown> : {};
  return {
    access_token: data.access_token,
    refresh_token: typeof data.refresh_token === 'string' ? data.refresh_token : undefined,
    expires_at: new Date(Date.now() + expiresIn * 1000).toISOString(),
    email: typeof account.email_address === 'string' ? account.email_address : undefined,
    account_uuid: typeof account.uuid === 'string' ? account.uuid : undefined,
  };
}

export async function refreshClaudeToken(refreshToken: string): Promise<ClaudeTokenData> {
  const response = await fetch(CLAUDE_TOKEN_URL, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      client_id: CLAUDE_CLIENT_ID,
      grant_type: 'refresh_token',
      refresh_token: refreshToken,
    }),
  });
  if (!response.ok) throw new Error(`claude_token_refresh_failed_${response.status}`);
  const data = await response.json() as Record<string, unknown>;
  if (typeof data.access_token !== 'string' || !data.access_token) throw new Error('claude_missing_access_token');
  const expiresIn = typeof data.expires_in === 'number' ? data.expires_in : 3600;
  return {
    access_token: data.access_token,
    refresh_token: typeof data.refresh_token === 'string' ? data.refresh_token : undefined,
    expires_at: new Date(Date.now() + expiresIn * 1000).toISOString(),
  };
}
