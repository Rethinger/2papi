import crypto from 'node:crypto';
import { z } from 'zod';
import { env } from './env';

// OIDC (Authorization Code + PKCE-free v1) for the dashboard — Enterprise
// feature "sso" (docs/strategy-v3.md, шаг 4 хребта; LiteLLM-якорь).
// Single IdP per deployment, config lives in system_settings.oidc.
// No network at import time; all HTTP goes through an injectable fetch.

export const OidcConfigSchema = z.object({
  issuer: z.string().url(),
  client_id: z.string().min(1),
  client_secret: z.string().min(1),
  scopes: z.array(z.string().min(1)).default(['openid', 'email', 'profile']),
  redirect_uri: z.string().url().optional(),
});
export type OidcConfig = z.infer<typeof OidcConfigSchema>;

export interface Discovery {
  authorization_endpoint: string;
  token_endpoint: string;
  jwks_uri: string;
  issuer: string;
}

type FetchImpl = (input: string, init?: RequestInit) => Promise<Response>;
export type { FetchImpl };

export async function discover(issuer: string, fetchImpl: FetchImpl = fetch): Promise<Discovery> {
  const url = `${issuer.replace(/\/$/, '')}/.well-known/openid-configuration`;
  const res = await fetchImpl(url);
  if (!res.ok) throw new Error(`oidc_discovery_failed (${res.status})`);
  const body = await res.json() as Record<string, unknown>;
  for (const key of ['authorization_endpoint', 'token_endpoint', 'jwks_uri']) {
    if (typeof body[key] !== 'string') throw new Error(`oidc_discovery_missing_${key}`);
  }
  return {
    issuer: String(body.issuer ?? issuer),
    authorization_endpoint: String(body.authorization_endpoint),
    token_endpoint: String(body.token_endpoint),
    jwks_uri: String(body.jwks_uri),
  };
}

export function buildAuthUrl(discovery: Discovery, config: OidcConfig, redirectUri: string, state: string): string {
  const url = new URL(discovery.authorization_endpoint);
  url.searchParams.set('response_type', 'code');
  url.searchParams.set('client_id', config.client_id);
  url.searchParams.set('redirect_uri', redirectUri);
  url.searchParams.set('scope', config.scopes.join(' '));
  url.searchParams.set('state', state);
  return url.toString();
}

interface JwkKey {
  kid?: string;
  kty: string;
  alg?: string;
  n?: string;
  e?: string;
  use?: string;
}

function jwkToKeyObject(jwk: JwkKey): crypto.KeyObject {
  if (jwk.kty !== 'RSA' || !jwk.n || !jwk.e) throw new Error('oidc_jwk_unsupported');
  return crypto.createPublicKey({ key: jwk as unknown as JsonWebKey, format: 'jwk' });
}

const RSA_HASH: Record<string, string> = {
  RS256: 'RSA-SHA256',
  RS384: 'RSA-SHA384',
  RS512: 'RSA-SHA512',
};

export interface IdTokenClaims {
  sub: string;
  email?: string;
  email_verified?: boolean;
  [key: string]: unknown;
}

// verifyIdToken checks signature + iss/aud/exp/nonce. RS* via JWKS,
// HS256 via client_secret (IdPs that sign symmetric).
export async function verifyIdToken(
  idToken: string,
  opts: { discovery: Discovery; clientId: string; clientSecret: string; nonce?: string; now?: Date; fetchImpl?: FetchImpl },
): Promise<IdTokenClaims> {
  const parts = idToken.split('.');
  if (parts.length !== 3) throw new Error('oidc_token_malformed');
  const header = JSON.parse(Buffer.from(parts[0], 'base64url').toString('utf8')) as { alg: string; kid?: string };
  const claims = JSON.parse(Buffer.from(parts[1], 'base64url').toString('utf8')) as IdTokenClaims & { exp?: number; aud?: unknown };
  const signingInput = `${parts[0]}.${parts[1]}`;
  const sig = Buffer.from(parts[2], 'base64url');

  let valid = false;
  if (header.alg in RSA_HASH) {
    const res = await (opts.fetchImpl ?? fetch)(opts.discovery.jwks_uri);
    if (!res.ok) throw new Error(`oidc_jwks_fetch_failed (${res.status})`);
    const jwks = await res.json() as { keys?: JwkKey[] };
    const candidates = (jwks.keys ?? []).filter(k => k.kty === 'RSA' && (!k.use || k.use === 'sig') && (!header.kid || !k.kid || k.kid === header.kid));
    for (const jwk of candidates) {
      try {
        if (crypto.verify(RSA_HASH[header.alg], Buffer.from(signingInput), jwkToKeyObject(jwk), sig)) { valid = true; break; }
      } catch { /* try next key */ }
    }
  } else if (header.alg === 'HS256') {
    const expected = crypto.createHmac('sha256', opts.clientSecret).update(signingInput).digest();
    valid = expected.length === sig.length && crypto.timingSafeEqual(expected, sig);
  } else {
    throw new Error(`oidc_alg_unsupported:${header.alg}`);
  }
  if (!valid) throw new Error('oidc_token_bad_signature');

  const now = Math.floor((opts.now ?? new Date()).getTime() / 1000);
  if (!claims.exp || now >= claims.exp + 60) throw new Error('oidc_token_expired');
  if ((opts.discovery.issuer.replace(/\/$/, '')) !== String(claims.iss ?? '').replace(/\/$/, '')) throw new Error('oidc_token_iss_mismatch');
  const auds = Array.isArray(claims.aud) ? claims.aud : [claims.aud];
  if (!auds.includes(opts.clientId)) throw new Error('oidc_token_aud_mismatch');
  if (opts.nonce !== undefined && claims.nonce !== opts.nonce) throw new Error('oidc_nonce_mismatch');
  return claims;
}

// Signed CSRF state for the authorization round-trip:
// "<b64(payload)>.<hmac>" where payload = {n: nonce, iat}. Master key signs
// it, so no server-side storage is needed; TTL is enforced on read.
export function issueState(): { state: string; nonce: string } {
  const nonce = crypto.randomBytes(16).toString('base64url');
  const payload = Buffer.from(JSON.stringify({ n: nonce, iat: Date.now() })).toString('base64url');
  const mac = crypto.createHmac('sha256', masterSecret()).update(payload).digest('base64url');
  return { state: `${payload}.${mac}`, nonce };
}

export function readState(state: string | null | undefined, ttlMs = 10 * 60_000): { nonce: string } | null {
  if (!state) return null;
  const dot = state.lastIndexOf('.');
  if (dot <= 0) return null;
  const payload = state.slice(0, dot);
  const expected = crypto.createHmac('sha256', masterSecret()).update(payload).digest('base64url');
  const got = state.slice(dot + 1);
  const a = Buffer.from(got);
  const b = Buffer.from(expected);
  if (a.length !== b.length || !crypto.timingSafeEqual(a, b)) return null;
  let parsed: { n?: string; iat?: number };
  try {
    parsed = JSON.parse(Buffer.from(payload, 'base64url').toString('utf8'));
  } catch {
    return null;
  }
  if (!parsed.n || !parsed.iat || Date.now() - parsed.iat > ttlMs) return null;
  return { nonce: parsed.n };
}

function masterSecret(): string {
  return env.CONTROL_PLANE_MASTER_KEY;
}
