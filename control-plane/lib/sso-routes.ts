import { z } from 'zod';
import { pool as defaultPool, type Queryable } from './db';
import { problem, ApiError } from './api';
import { env } from './env';
import { requireFeature } from './edition';
import { OidcConfigSchema, OidcConfig, discover, buildAuthUrl, verifyIdToken, issueState, readState, type FetchImpl } from './oidc';
import { createSession, sessionCookie, findOrCreateOidcUser } from './auth';

// SSO/OIDC dashboard login (Enterprise "sso", шаг 4 хребта). Core logic is
// separated from Next route wrappers so tests run without the Next runtime.

export const OidcSettingsSchema = OidcConfigSchema;

const STATE_COOKIE = 'papi_oidc_state';

function stateCookie(state: string): string {
  return `${STATE_COOKIE}=${state}; Path=/; HttpOnly; SameSite=Lax; Max-Age=600`;
}
const CLEAR_STATE_COOKIE = `${STATE_COOKIE}=; Path=/; HttpOnly; SameSite=Lax; Max-Age=0`;

export async function getOidcConfig(db: Queryable): Promise<OidcConfig | null> {
  const row = (await db.query(`SELECT value FROM system_settings WHERE key='oidc'`)).rows[0];
  if (!row?.value) return null;
  const parsed = OidcSettingsSchema.safeParse(row.value);
  return parsed.success ? parsed.data : null;
}

async function saveOidcConfig(db: Queryable, config: OidcConfig) {
  await db.query(
    `INSERT INTO system_settings (key,value,updated_at) VALUES ('oidc',$1,now())
     ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=now()`,
    [JSON.stringify(config)],
  );
}

export interface SsoRouteDeps {
  pool?: Queryable;
  fetchImpl?: FetchImpl;
  now?: Date;
  originOverride?: string;
}

export function ssoRouteDeps(): SsoRouteDeps {
  return { pool: defaultPool };
}

function redirectUriFor(req: Request, deps: SsoRouteDeps, config: OidcConfig): string {
  if (config.redirect_uri) return config.redirect_uri;
  const base = deps.originOverride ?? env.DASHBOARD_PUBLIC_URL ?? new URL(req.url).origin;
  return `${base.replace(/\/$/, '')}/api/auth/oidc/callback`;
}

function redirectBase(deps: SsoRouteDeps, req: Request): string {
  return (deps.originOverride ?? env.DASHBOARD_PUBLIC_URL ?? new URL(req.url).origin).replace(/\/$/, '');
}

// GET /api/auth/oidc/start — redirect to the IdP with a signed state.
export async function oidcStartCore(req: Request, deps: SsoRouteDeps = ssoRouteDeps()) {
  try {
    requireFeature('sso');
    const db = deps.pool!;
    const config = await getOidcConfig(db);
    if (!config) throw new ApiError(409, 'sso_not_configured', 'OIDC is not configured for this deployment');
    const discovery = await discover(config.issuer, deps.fetchImpl);
    const { state, nonce } = issueState();
    const url = buildAuthUrl(discovery, config, redirectUriFor(req, deps, config), state);
    return new Response(null, { status: 302, headers: { location: url, 'set-cookie': stateCookie(state) } });
  } catch (e) {
    return problem(e);
  }
}

const CallbackQuerySchema = z.object({
  code: z.string().min(1).optional(),
  state: z.string().min(1).optional(),
  error: z.string().optional(),
});

// GET /api/auth/oidc/callback — verify state, exchange code, validate the
// ID token (nonce/iss/aud/exp), provision user, issue a session cookie.
export async function oidcCallbackCore(req: Request, deps: SsoRouteDeps = ssoRouteDeps()) {
  try {
    requireFeature('sso');
    const db = deps.pool!;
    const url = new URL(req.url);
    const query = CallbackQuerySchema.parse(Object.fromEntries(url.searchParams));
    const base = redirectBase(deps, req);

    const fail = (status: number, code: string, message: string) =>
      Response.json({ error: { code, message } }, { status, headers: { 'set-cookie': CLEAR_STATE_COOKIE } });

    if (query.error) return fail(401, 'sso_provider_error', `Identity provider returned ${query.error}`);
    if (!query.code || !query.state) return fail(400, 'sso_missing_params', 'code and state are required');

    // The browser cookie must carry the same signed state.
    const cookieState = req.headers.get('cookie')?.match(/(?:^|;\s*)papi_oidc_state=([^;]+)/)?.[1];
    if (!cookieState || cookieState !== query.state) return fail(401, 'sso_state_mismatch', 'SSO state does not match');
    const state = readState(cookieState);
    if (!state) return fail(401, 'sso_state_expired', 'SSO state expired or invalid');

    const config = await getOidcConfig(db);
    if (!config) throw new ApiError(409, 'sso_not_configured', 'OIDC is not configured for this deployment');
    const discovery = await discover(config.issuer, deps.fetchImpl);

    const tokenRes = await (deps.fetchImpl ?? fetch)(discovery.token_endpoint, {
      method: 'POST',
      headers: { 'content-type': 'application/x-www-form-urlencoded' },
      body: new URLSearchParams({
        grant_type: 'authorization_code',
        code: query.code,
        redirect_uri: redirectUriFor(req, deps, config),
        client_id: config.client_id,
        client_secret: config.client_secret,
      }),
    });
    if (!tokenRes.ok) throw new ApiError(401, 'sso_token_exchange_failed', `Token endpoint rejected the code (${tokenRes.status})`);
    const tokens = await tokenRes.json() as { id_token?: string };
    if (!tokens.id_token) throw new ApiError(401, 'sso_token_missing', 'IdP did not return an id_token');

    const claims = await verifyIdToken(tokens.id_token, {
      discovery,
      clientId: config.client_id,
      clientSecret: config.client_secret,
      nonce: state.nonce,
      now: deps.now,
      fetchImpl: deps.fetchImpl,
    });

    if (typeof claims.email !== 'string' || !claims.email.includes('@')) {
      throw new ApiError(403, 'sso_email_missing', 'The IdP did not provide an email claim');
    }
    if (claims.email_verified === false) {
      throw new ApiError(403, 'sso_email_unverified', 'Verify your email with the identity provider first');
    }

    const user = await findOrCreateOidcUser(db, claims.email);
    if (user.disabled_at) throw new ApiError(403, 'sso_user_disabled', 'This account has been suspended');
    const session = await createSession(db, user.id);
    const headers = new Headers({ location: `${base}/` });
    headers.append('set-cookie', sessionCookie(session));
    headers.append('set-cookie', CLEAR_STATE_COOKIE);
    return new Response(null, { status: 302, headers });
  } catch (e) {
    return problem(e);
  }
}

// Operator config endpoints used by the catch-all control route:
export async function getOidcStatus(db: Queryable) {
  const config = await getOidcConfig(db);
  return {
    enabled: Boolean(config),
    ...(config ? { issuer: config.issuer, client_id: config.client_id, scopes: config.scopes, redirect_uri: config.redirect_uri ?? null } : {}),
    client_secret_set: Boolean(config),
  };
}

export async function saveOidcSettings(db: Queryable, body: unknown) {
  requireFeature('sso');
  const v = OidcSettingsSchema.parse(body);
  await saveOidcConfig(db, v);
  return getOidcStatus(db);
}
