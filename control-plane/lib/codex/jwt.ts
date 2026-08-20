import crypto from 'node:crypto';
import { codexAuthOrigin, codexUrls, CODEX_CLIENT_ID } from './constants';

export type VerifiedCodexIdentity = { sub: string; chatgpt_account_id: string; chatgpt_user_id?: string; email?: string; plan_type?: string; exp: number };

type Jwk = JsonWebKey & { kid?: string; alg?: string; kty?: string };
let cache: { expires: number; keys: Jwk[] } | undefined;

function b64url(input: string) { return Buffer.from(input.replace(/-/g, '+').replace(/_/g, '/'), 'base64'); }
function parsePart<T>(part: string): T { return JSON.parse(b64url(part).toString('utf8')) as T; }

export function unsafeReadJwtExp(token: string): number | undefined {
  try {
    const parts = token.split('.');
    if (parts.length !== 3) return undefined;
    const payload = parsePart<Record<string, unknown>>(parts[1]);
    return typeof payload.exp === 'number' ? payload.exp : undefined;
  } catch { return undefined; }
}

async function loadJwks(force = false): Promise<Jwk[]> {
  if (!force && cache && cache.expires > Date.now()) return cache.keys;
  const res = await fetch(codexUrls().jwks);
  if (!res.ok) throw new Error('jwks_fetch_failed');
  const body = await res.json() as { keys?: Jwk[] };
  const keys = Array.isArray(body.keys) ? body.keys : [];
  cache = { expires: Date.now() + 60 * 60 * 1000, keys };
  return keys;
}

export function clearJwksCacheForTests() { cache = undefined; }

async function verifyOpenAIToken(token: string, audiences: readonly string[], nonce?: string): Promise<VerifiedCodexIdentity> {
  const parts = token.split('.');
  if (parts.length !== 3) throw new Error('invalid_id_token');
  const header = parsePart<{ alg?: string; kid?: string }>(parts[0]);
  const payload = parsePart<Record<string, unknown>>(parts[1]);
  if (header.alg !== 'RS256' || !header.kid) throw new Error('unsupported_id_token_alg');
  let keys = await loadJwks();
  let jwk = keys.find(k => k.kid === header.kid);
  if (!jwk) { keys = await loadJwks(true); jwk = keys.find(k => k.kid === header.kid); }
  if (!jwk) throw new Error('unknown_jwks_kid');
  const ok = crypto.verify('RSA-SHA256', Buffer.from(`${parts[0]}.${parts[1]}`), crypto.createPublicKey({ key: jwk, format: 'jwk' }), b64url(parts[2]));
  if (!ok) throw new Error('invalid_id_token_signature');
  if (payload.iss !== codexAuthOrigin()) throw new Error('invalid_id_token_issuer');
  const aud = payload.aud;
  if (!(typeof aud === 'string' ? audiences.includes(aud) : Array.isArray(aud) && aud.some(value => typeof value === 'string' && audiences.includes(value)))) throw new Error('invalid_id_token_audience');
  if (typeof payload.exp !== 'number' || payload.exp <= Math.floor(Date.now() / 1000)) throw new Error('expired_id_token');
  if (nonce !== undefined && payload.nonce !== nonce) throw new Error('invalid_id_token_nonce');
  const auth = payload['https://api.openai.com/auth'];
  if (!auth || typeof auth !== 'object' || Array.isArray(auth)) throw new Error('missing_openai_auth_claim');
  const structured = auth as Record<string, unknown>;
  const account = typeof structured.chatgpt_account_id === 'string' ? structured.chatgpt_account_id : '';
  if (!account) throw new Error('missing_chatgpt_account_id');
  const user = typeof structured.chatgpt_user_id === 'string' ? structured.chatgpt_user_id : undefined;
  return { sub: user ?? account, chatgpt_account_id: account, chatgpt_user_id: user, email: typeof structured.email === 'string' ? structured.email : undefined, plan_type: typeof structured.chatgpt_plan_type === 'string' ? structured.chatgpt_plan_type : undefined, exp: payload.exp };
}

export function verifyOpenAIIDToken(token: string, nonce?: string) {
  return verifyOpenAIToken(token, [CODEX_CLIENT_ID], nonce);
}

export function verifyOpenAIAccessToken(token: string) {
  return verifyOpenAIToken(token, ['https://api.openai.com/v1']);
}
