import crypto from 'node:crypto';
import { codexAuthOrigin, codexUrls, CODEX_CLIENT_ID } from './constants';

export type VerifiedCodexIdentity = { sub: string; chatgpt_account_id: string; email?: string; plan_type?: string; exp: number };

type Jwk = JsonWebKey & { kid?: string; alg?: string; kty?: string };
let cache: { expires: number; keys: Jwk[] } | undefined;

function b64url(input: string) { return Buffer.from(input.replace(/-/g, '+').replace(/_/g, '/'), 'base64'); }
function parsePart<T>(part: string): T { return JSON.parse(b64url(part).toString('utf8')) as T; }

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

export async function verifyOpenAIIDToken(token: string, nonce: string): Promise<VerifiedCodexIdentity> {
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
  if (!(aud === CODEX_CLIENT_ID || (Array.isArray(aud) && aud.includes(CODEX_CLIENT_ID)))) throw new Error('invalid_id_token_audience');
  if (typeof payload.exp !== 'number' || payload.exp <= Math.floor(Date.now() / 1000)) throw new Error('expired_id_token');
  if (payload.nonce !== nonce) throw new Error('invalid_id_token_nonce');
  const account = String(payload['https://api.openai.com/auth'] ?? payload.chatgpt_account_id ?? payload.sub ?? '');
  if (!account) throw new Error('missing_chatgpt_account_id');
  return { sub: String(payload.sub ?? account), chatgpt_account_id: account, email: typeof payload.email === 'string' ? payload.email : undefined, plan_type: typeof payload.plan === 'string' ? payload.plan : typeof payload.plan_type === 'string' ? payload.plan_type : undefined, exp: payload.exp };
}
