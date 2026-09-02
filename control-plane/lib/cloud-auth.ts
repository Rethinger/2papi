import crypto from 'node:crypto';
import { z } from 'zod';
import { pool as defaultPool, txOn, type Queryable } from './db';
import { problem, ApiError } from './api';
import { env } from './env';
import { requireHosted } from './edition';
import { rateLimit, clientIp } from './rate-limit';
import { createSession, sessionCookie, clearSessionCookie, resolveSession } from './auth';
import { hashPassword, verifyPassword } from './passwords';

// Шаг 6 хребта (Cloud): self-serve signup → email verification (grant $1–3
// + personal team + first key) → login. Core logic separated from Next
// route wrappers; email delivery itself is operator wiring (SMTP later) —
// the token is stored hashed and verifiable via POST /api/auth/verify.

const SignupSchema = z.object({
  email: z.string().trim().toLowerCase().email(),
  password: z.string().min(10).max(200),
});
const LoginSchema = z.object({
  email: z.string().trim().toLowerCase().email(),
  password: z.string().min(1).max(200),
});

export interface CloudAuthDeps {
  pool?: Queryable;
  now?: Date;
}

export function cloudAuthDeps(): CloudAuthDeps {
  return { pool: defaultPool };
}

function hashToken(token: string): string {
  return crypto.createHash('sha256').update(token).digest('hex');
}

// POST /api/auth/signup — create an unverified user and a verification
// token. The response is deliberately identical whether or not the account
// existed (no enumeration).
export async function signupCore(req: Request, deps: CloudAuthDeps = cloudAuthDeps()) {
  try {
    requireHosted();
    const ip = clientIp(req);
    if (!rateLimit(`signup:${ip}`, Number(process.env.SIGNUP_RATE_LIMIT ?? 5), 3600_000)) {
      throw new ApiError(429, 'rate_limited', 'Too many signup attempts. Try again later.');
    }
    const db = deps.pool!;
    const body = SignupSchema.parse(await req.json());
    const existing = (await db.query('SELECT id FROM users WHERE lower(email)=$1', [body.email])).rows[0];
    if (!existing) {
      const user = (await db.query(
        'INSERT INTO users (email, password_hash) VALUES ($1,$2) RETURNING id',
        [body.email, hashPassword(body.password)],
      )).rows[0];
      await issueVerificationToken(db, user.id);
    }
    return Response.json({ data: { ok: true } }, { status: 200 });
  } catch (e) {
    return problem(e);
  }
}

async function issueVerificationToken(db: Queryable, userId: string): Promise<string> {
  const token = crypto.randomBytes(32).toString('base64url');
  const expiresAt = new Date(Date.now() + env.VERIFICATION_TOKEN_TTL_HOURS * 3600_000);
  // One live token per user: a fresh signup request invalidates the old one.
  await db.query('DELETE FROM email_verification_tokens WHERE user_id=$1', [userId]);
  await db.query(
    'INSERT INTO email_verification_tokens (user_id, token_hash, expires_at) VALUES ($1,$2,$3)',
    [userId, hashToken(token), expiresAt],
  );
  return token;
}

// Verification provisions everything the tenant needs on first login:
// personal team (trust_tier 0) + owner membership + first virtual key +
// the signup credit grant — all in one transaction.
export async function verifyCore(req: Request, deps: CloudAuthDeps = cloudAuthDeps()) {
  try {
    requireHosted();
    const ip = clientIp(req);
    if (!rateLimit(`verify:${ip}`, Number(process.env.VERIFY_RATE_LIMIT ?? 20), 3600_000)) {
      throw new ApiError(429, 'rate_limited', 'Too many verification attempts. Try again later.');
    }
    const db = deps.pool!;
    const body = z.object({ token: z.string().min(16).max(200) }).parse(await req.json());
    const row = (await db.query(
      `SELECT t.user_id, u.email, u.email_verified_at FROM email_verification_tokens t
       JOIN users u ON u.id = t.user_id
       WHERE t.token_hash=$1 AND t.expires_at >= now()`,
      [hashToken(body.token)],
    )).rows[0];
    if (!row) throw new ApiError(400, 'invalid_verification_token', 'Verification link is invalid or expired');
    if (row.email_verified_at) {
      await db.query('DELETE FROM email_verification_tokens WHERE user_id=$1', [row.user_id]);
      return Response.json({ data: { ok: true, already_verified: true } });
    }

    const bonus = env.SIGNUP_BONUS_USD;
    await txOn(db, async client => {
      await client.query('UPDATE users SET email_verified_at=now(), updated_at=now() WHERE id=$1', [row.user_id]);
      await client.query('DELETE FROM email_verification_tokens WHERE user_id=$1', [row.user_id]);

      const team = (await client.query(
        `INSERT INTO teams (name, enabled, budget_usd, balance_usd) VALUES ($1, true, 0, $2) RETURNING id`,
        [row.email, bonus],
      )).rows[0];
      await client.query(`INSERT INTO team_members (team_id, user_id, role) VALUES ($1,$2,'owner')`, [team.id, row.user_id]);
      if (bonus > 0) {
        // Idempotent by UNIQUE(source, external_id): a retried verification
        // can never double-grant.
        await client.query(
          `INSERT INTO credit_transactions (team_id, delta_usd, kind, source, external_id)
           VALUES ($1,$2,'bonus','signup_bonus',$3)`,
          [team.id, bonus, row.user_id],
        );
      }
      const plaintext = `sk-cp-${crypto.randomBytes(24).toString('base64url')}`;
      const keyHash = crypto.createHmac('sha256', process.env.GATEWAY_SHARED_SECRET ?? 'dev-secret-change-me').update(plaintext).digest('hex');
      await client.query(
        `INSERT INTO virtual_keys (name,key_hash,key_prefix,enabled,models,rpm,team_id,budget_usd)
         VALUES ('default',$1,'sk-cp',true,'{}',60,$2,0)`,
        [keyHash, team.id],
      );
    });
    return Response.json({ data: { ok: true } });
  } catch (e) {
    return problem(e);
  }
}

// POST /api/auth/login — password login for verified accounts.
export async function loginCore(req: Request, deps: CloudAuthDeps = cloudAuthDeps()) {
  try {
    requireHosted();
    const ip = clientIp(req);
    if (!rateLimit(`login:${ip}`, Number(process.env.LOGIN_RATE_LIMIT ?? 10), 3600_000)) {
      throw new ApiError(429, 'rate_limited', 'Too many login attempts. Try again later.');
    }
    const db = deps.pool!;
    const body = LoginSchema.parse(await req.json());
    const user = (await db.query('SELECT * FROM users WHERE lower(email)=$1', [body.email])).rows[0];
    // Same error shape for unknown user and bad password (no enumeration).
    if (!user || !verifyPassword(body.password, user.password_hash)) {
      throw new ApiError(401, 'invalid_credentials', 'Invalid email or password');
    }
    if (user.disabled_at) throw new ApiError(403, 'account_disabled', 'This account has been suspended');
    if (!user.email_verified_at) throw new ApiError(403, 'email_unverified', 'Verify your email before signing in');

    const session = await createSession(db, user.id);
    return Response.json(
      { data: { ok: true, platform_role: user.platform_role } },
      { headers: { 'set-cookie': sessionCookie(session) } },
    );
  } catch (e) {
    return problem(e);
  }
}

// GET data for the signed-in tenant's own team (balances + last ledger
// rows). Operator view is separate (/api/control/v1/billing); tenants never
// see other teams.
export async function billingCore(req: Request, deps: CloudAuthDeps = cloudAuthDeps()) {
  try {
    requireHosted();
    const db = deps.pool!;
    const token = req.headers.get('cookie')?.match(/(?:^|;\s*)papi_session=([^;]+)/)?.[1];
    const user = await resolveSession(db, token);
    if (!user) throw new ApiError(401, 'unauthorized', 'Not signed in');
    const team = (await db.query(
      `SELECT t.id, t.name, t.balance_usd FROM teams t
       JOIN team_members tm ON tm.team_id = t.id WHERE tm.user_id=$1 ORDER BY t.created_at LIMIT 1`,
      [user.id],
    )).rows[0];
    if (!team) return Response.json({ data: { team: null, balance_usd: 0, transactions: [] } });
    const transactions = (await db.query(
      `SELECT delta_usd, kind, source, external_id, note, created_at
       FROM credit_transactions WHERE team_id=$1 ORDER BY created_at DESC LIMIT 50`,
      [team.id],
    )).rows;
    const keys = (await db.query(
      `SELECT name, key_prefix, enabled FROM virtual_keys WHERE team_id=$1 AND enabled ORDER BY created_at`,
      [team.id],
    )).rows;
    return Response.json({
      data: {
        team: { id: team.id, name: team.name },
        balance_usd: Number(team.balance_usd),
        transactions,
        keys,
        checkout_url: process.env.PADDLE_CHECKOUT_URL ?? '',
      },
    });
  } catch (e) {
    return problem(e);
  }
}

// POST /api/auth/logout — drop the current session.
export async function logoutCore(req: Request, deps: CloudAuthDeps = cloudAuthDeps()) {
  try {
    requireHosted();
    const token = req.headers.get('cookie')?.match(/(?:^|;\s*)papi_session=([^;]+)/)?.[1];
    if (token) await deps.pool!.query('DELETE FROM user_sessions WHERE token_hash=$1', [hashToken(token)]);
    return Response.json({ data: { ok: true } }, { headers: { 'set-cookie': clearSessionCookie() } });
  } catch (e) {
    return problem(e);
  }
}

// POST /api/auth/token — exchange the operator session cookie for a
// short-lived bearer JWT for programmatic access to /api/control/v1.
export async function tokenCore(req: Request, deps: CloudAuthDeps = cloudAuthDeps()) {
  try {
    requireHosted();
    const db = deps.pool!;
    const token = req.headers.get('cookie')?.match(/(?:^|;\s*)papi_session=([^;]+)/)?.[1];
    const user = await resolveSession(db, token);
    if (!user) throw new ApiError(401, 'unauthorized', 'Not signed in');
    if (user.platform_role !== 'operator') throw new ApiError(403, 'operator_required', 'Only operators can mint API tokens');
    const { signJwt } = await import('./jwt');
    const ttl = Number(process.env.API_TOKEN_TTL_SECONDS ?? 3600);
    return Response.json({
      data: {
        access_token: signJwt({ sub: user.id, role: 'operator' }, env.CONTROL_PLANE_MASTER_KEY, Number.isFinite(ttl) && ttl > 0 ? ttl : 3600),
        token_type: 'Bearer',
        expires_in: Number.isFinite(ttl) && ttl > 0 ? ttl : 3600,
      },
    });
  } catch (e) {
    return problem(e);
  }
}

// GET /api/auth/me — who am I (used by the dashboard once auth exists).
export async function meCore(req: Request, deps: CloudAuthDeps = cloudAuthDeps()) {
  try {
    requireHosted();
    const token = req.headers.get('cookie')?.match(/(?:^|;\s*)papi_session=([^;]+)/)?.[1];
    const user = await resolveSession(deps.pool!, token);
    if (!user) throw new ApiError(401, 'unauthorized', 'Not signed in');
    const team = (await deps.pool!.query(
      `SELECT t.id, t.name, t.balance_usd, tm.role FROM teams t
       JOIN team_members tm ON tm.team_id = t.id WHERE tm.user_id=$1 ORDER BY t.created_at LIMIT 1`,
      [user.id],
    )).rows[0];
    return Response.json({ data: { ...user, team: team ?? null } });
  } catch (e) {
    return problem(e);
  }
}
