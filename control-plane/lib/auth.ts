import crypto from 'node:crypto';
import type { Pool, PoolClient } from 'pg';

// Dashboard user sessions (migration 011 user_sessions). Tokens are random,
// stored as SHA-256 hashes; the plaintext lives only in the cookie.
// Shared by password login (шаг 6) and OIDC login (шаг 4).

export const SESSION_COOKIE = 'papi_session';
const DEFAULT_TTL_DAYS = 7;

export interface IssuedSession {
  token: string;
  expiresAt: Date;
}

function hashToken(token: string): string {
  return crypto.createHash('sha256').update(token).digest('hex');
}

export async function createSession(
  db: Pool | PoolClient,
  userId: string,
  ttlDays = Number(process.env.SESSION_TTL_DAYS ?? DEFAULT_TTL_DAYS),
): Promise<IssuedSession> {
  const token = crypto.randomBytes(32).toString('base64url');
  const ttl = Number.isFinite(ttlDays) && ttlDays > 0 ? ttlDays : DEFAULT_TTL_DAYS;
  const expiresAt = new Date(Date.now() + ttl * 86400_000);
  await db.query(
    'INSERT INTO user_sessions (user_id, token_hash, expires_at) VALUES ($1,$2,$3)',
    [userId, hashToken(token), expiresAt],
  );
  return { token, expiresAt };
}

export function sessionCookie(session: IssuedSession): string {
  const parts = [
    `${SESSION_COOKIE}=${session.token}`,
    'Path=/',
    'HttpOnly',
    'SameSite=Lax',
    `Expires=${session.expiresAt.toUTCString()}`,
  ];
  if (process.env.NODE_ENV === 'production') parts.push('Secure');
  return parts.join('; ');
}

export function clearSessionCookie(): string {
  return `${SESSION_COOKIE}=; Path=/; HttpOnly; SameSite=Lax; Max-Age=0`;
}

export interface SessionUser {
  id: string;
  email: string;
  platform_role: string;
}

// resolveSession returns the user for a live session token, or null when
// unknown/expired/disabled. Expired rows are swept lazily on lookup.
export async function resolveSession(db: Pool | PoolClient, token: string | null | undefined): Promise<SessionUser | null> {
  if (!token) return null;
  const sweep = await db.query('DELETE FROM user_sessions WHERE expires_at < now() AND token_hash = $1', [hashToken(token)]);
  if ((sweep.rowCount ?? 0) > 0) return null;
  const row = (await db.query(
    `SELECT u.id, u.email, u.platform_role FROM user_sessions s
     JOIN users u ON u.id = s.user_id
     WHERE s.token_hash = $1 AND s.expires_at >= now() AND u.disabled_at IS NULL`,
    [hashToken(token)],
  )).rows[0];
  return row ? { id: row.id, email: row.email, platform_role: row.platform_role } : null;
}

// findOrCreateOidcUser provisions a dashboard account from trusted IdP
// claims. IdP-verified emails are marked verified; created accounts have no
// usable password ('!' prefix never matches any verifier in шаг 6).
export async function findOrCreateOidcUser(db: Pool | PoolClient, email: string): Promise<{ id: string } & Record<string, any>> {
  const existing = (await db.query('SELECT * FROM users WHERE lower(email) = lower($1)', [email])).rows[0];
  if (existing) {
    if (!existing.email_verified_at) {
      await db.query('UPDATE users SET email_verified_at = now(), updated_at = now() WHERE id = $1', [existing.id]);
      existing.email_verified_at = new Date();
    }
    return existing;
  }
  return (await db.query(
    `INSERT INTO users (email, password_hash, email_verified_at) VALUES ($1,$2,now()) RETURNING *`,
    [email.toLowerCase(), '!oidc-' + crypto.randomBytes(24).toString('hex')],
  )).rows[0];
}
