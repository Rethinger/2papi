import crypto from 'node:crypto';

// Minimal HS256 JWT for programmatic operator access to the control API
// (Enterprise "JWT-auth for API", strategy-v3 п.1). Tokens are short-lived,
// signed with CONTROL_PLANE_MASTER_KEY and scoped to audience '2papi-api'.

export interface JwtClaims {
  sub: string;
  role: string;
  aud: string;
  iat: number;
  exp: number;
}

const AUDIENCE = '2papi-api';
const b64u = (obj: unknown) => Buffer.from(JSON.stringify(obj)).toString('base64url');

function hmac(input: string, secret: string): string {
  return crypto.createHmac('sha256', secret).update(input).digest('base64url');
}

export function signJwt(claims: Omit<JwtClaims, 'aud' | 'iat' | 'exp'>, secret: string, ttlSeconds: number, now = Date.now()): string {
  const iat = Math.floor(now / 1000);
  const payload: JwtClaims = { ...claims, aud: AUDIENCE, iat, exp: iat + Math.max(1, Math.floor(ttlSeconds)) };
  const input = `${b64u({ alg: 'HS256', typ: 'JWT' })}.${b64u(payload)}`;
  return `${input}.${hmac(input, secret)}`;
}

export interface VerifiedJwt extends JwtClaims {}

export function verifyJwt(token: string, secret: string, now = Date.now()): VerifiedJwt | null {
  const parts = token.split('.');
  if (parts.length !== 3) return null;
  const [headerB64, payloadB64, sigB64] = parts;
  let header: { alg?: string };
  try {
    header = JSON.parse(Buffer.from(headerB64, 'base64url').toString('utf8'));
  } catch {
    return null;
  }
  if (header.alg !== 'HS256') return null; // no alg-swap games
  const expected = hmac(`${headerB64}.${payloadB64}`, secret);
  const a = Buffer.from(sigB64);
  const b = Buffer.from(expected);
  if (a.length !== b.length || !crypto.timingSafeEqual(a, b)) return null;

  let claims: JwtClaims & { iss?: string };
  try {
    claims = JSON.parse(Buffer.from(payloadB64, 'base64url').toString('utf8'));
  } catch {
    return null;
  }
  const nowSec = Math.floor(now / 1000);
  if (!claims.exp || nowSec >= claims.exp + 30) return null; // 30s skew
  if (claims.aud !== AUDIENCE) return null;
  if (!claims.sub || typeof claims.role !== 'string') return null;
  return claims;
}

// Bearer extraction shared by the operator gate.
export function bearerFrom(req: Request): string | null {
  const h = req.headers.get('authorization');
  if (!h || !/^Bearer\s+/i.test(h)) return null;
  const value = h.replace(/^Bearer\s+/i, '').trim();
  return value || null;
}
