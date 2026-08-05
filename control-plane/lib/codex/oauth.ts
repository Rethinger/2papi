import crypto from 'node:crypto';
import { consumeJsonOnce, setJsonWithTtl } from '../redis';
import { CODEX_CLIENT_ID, CODEX_REDIRECT_URI, codexUrls } from './constants';
import { verifyOpenAIIDToken } from './jwt';
import type { NormalizedCodexCredential } from './auth-file';

export type OAuthSessionOptions = { accountName?: string; routing?: Record<string, unknown> };
type StoredSession = OAuthSessionOptions & { state: string; nonce: string; verifier: string; created_at: string };

function b64url(buf: Buffer) { return buf.toString('base64url'); }
function sessionKey(state: string) { return `codex:oauth:${state}`; }

export async function startOAuthSession(options: OAuthSessionOptions = {}, ttlSeconds = 600) {
  const state = b64url(crypto.randomBytes(32));
  const nonce = b64url(crypto.randomBytes(32));
  const verifier = b64url(crypto.randomBytes(32));
  const challenge = b64url(crypto.createHash('sha256').update(verifier).digest());
  await setJsonWithTtl(sessionKey(state), { ...options, state, nonce, verifier, created_at: new Date().toISOString() }, ttlSeconds);
  const url = new URL(codexUrls().authorize);
  url.searchParams.set('client_id', CODEX_CLIENT_ID);
  url.searchParams.set('redirect_uri', CODEX_REDIRECT_URI);
  url.searchParams.set('response_type', 'code');
  url.searchParams.set('scope', 'openid profile email offline_access');
  url.searchParams.set('state', state);
  url.searchParams.set('nonce', nonce);
  url.searchParams.set('code_challenge', challenge);
  url.searchParams.set('code_challenge_method', 'S256');
  return { state, authorizationUrl: url.toString(), expires_in: ttlSeconds };
}

export async function consumeOAuthSession(state: string) {
  return consumeJsonOnce<StoredSession>(sessionKey(state));
}

export async function exchangeAuthorizationCode(code: string, session: StoredSession): Promise<NormalizedCodexCredential> {
  const body = new URLSearchParams({ grant_type: 'authorization_code', client_id: CODEX_CLIENT_ID, code, redirect_uri: CODEX_REDIRECT_URI, code_verifier: session.verifier });
  const res = await fetch(codexUrls().token, { method: 'POST', headers: { 'content-type': 'application/x-www-form-urlencoded' }, body });
  if (!res.ok) throw new Error('codex_token_exchange_failed');
  const token = await res.json() as Record<string, unknown>;
  const idToken = typeof token.id_token === 'string' ? token.id_token : undefined;
  if (!idToken) throw new Error('missing_id_token');
  const identity = await verifyOpenAIIDToken(idToken, session.nonce);
  const expiresIn = typeof token.expires_in === 'number' ? token.expires_in : 3600;
  if (typeof token.access_token !== 'string' || !token.access_token) throw new Error('missing_access_token');
  return { kind: 'oauth', access_token: token.access_token, refresh_token: typeof token.refresh_token === 'string' ? token.refresh_token : undefined, id_token: idToken, expires_at: new Date(Date.now() + expiresIn * 1000).toISOString(), client_id: CODEX_CLIENT_ID, chatgpt_account_id: identity.chatgpt_account_id, email: identity.email, plan_type: identity.plan_type, auth_method: 'browser' };
}

export async function refreshOAuthCredential(refreshToken: string, clientId = CODEX_CLIENT_ID) {
  const body = new URLSearchParams({ grant_type: 'refresh_token', client_id: clientId, refresh_token: refreshToken });
  const res = await fetch(codexUrls().token, { method: 'POST', headers: { 'content-type': 'application/x-www-form-urlencoded' }, body });
  if (!res.ok) throw new Error('codex_refresh_failed');
  return await res.json() as Record<string, unknown>;
}
