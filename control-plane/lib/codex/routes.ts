import { tx } from '../db';
import { parseCodexAuthFile } from './auth-file';
import { startDeviceFlow, pollDeviceFlow, type DeviceFlowState } from './device';
import { exchangeAuthorizationCode, startOAuthSession, consumeOAuthSession, type OAuthSessionOptions } from './oauth';
import { createCodexAccount, type CreateCodexAccountInput } from './create-account';

const MAX_IMPORT_BYTES = 1024 * 1024;
const LOOPBACK_HOSTS = new Set(['localhost:1455', '127.0.0.1:1455']);

export type CodexRouteDeps = {
  dashboardPublicUrl: string;
  trustedProxy: boolean;
  startOAuthSession: (options?: OAuthSessionOptions & { accountId?: string }) => Promise<{ authorizationUrl: string; expires_in: number; state: string }>;
  consumeOAuthSession: typeof consumeOAuthSession;
  exchangeAuthorizationCode: typeof exchangeAuthorizationCode;
  parseCodexAuthFile: typeof parseCodexAuthFile;
  startDeviceFlow: typeof startDeviceFlow;
  pollDeviceFlow: typeof pollDeviceFlow;
  createAccount: (input: CreateCodexAccountInput) => Promise<{ id: string; revision: number }>;
  now?: () => Date;
};

export function codexRouteDeps(): CodexRouteDeps {
  return {
    dashboardPublicUrl: process.env.DASHBOARD_PUBLIC_URL || 'http://localhost:3000',
    trustedProxy: process.env.TRUSTED_PROXY === 'true',
    startOAuthSession,
    consumeOAuthSession,
    exchangeAuthorizationCode,
    parseCodexAuthFile,
    startDeviceFlow,
    pollDeviceFlow,
    createAccount: input => tx(client => createCodexAccount(client, input)),
  };
}

function json(body: unknown, status = 200) {
  return Response.json(body, { status });
}

function redirect(deps: CodexRouteDeps, status: string, accountId?: string) {
  const url = new URL(deps.dashboardPublicUrl);
  url.search = '';
  url.searchParams.set('codex_status', status);
  if (accountId) url.searchParams.set('account_id', accountId);
  return Response.redirect(url.toString(), 302);
}

function hostAllowed(request: Request, deps: CodexRouteDeps) {
  const directHost = request.headers.get('host') || new URL(request.url).host;
  if (!LOOPBACK_HOSTS.has(directHost)) return false;
  if (deps.trustedProxy) {
    const forwarded = request.headers.get('x-forwarded-host');
    if (forwarded && !LOOPBACK_HOSTS.has(forwarded)) return false;
  }
  return true;
}

export async function codexOAuthStartCore(request: Request, deps: CodexRouteDeps) {
  if (!hostAllowed(request, deps)) return json({ error: 'invalid_callback_host' }, 400);
  const body = await request.json().catch(() => ({})) as Record<string, unknown>;
  const started = await deps.startOAuthSession({ accountName: typeof body.name === 'string' ? body.name : undefined });
  return json({ authorization_url: started.authorizationUrl, expires_in: started.expires_in });
}

export async function codexReauthorizeCore(_request: Request, accountId: string, deps: CodexRouteDeps) {
  const started = await deps.startOAuthSession({ accountId });
  return json({ authorization_url: started.authorizationUrl, expires_in: started.expires_in });
}

export async function codexCallbackCore(request: Request, deps: CodexRouteDeps) {
  if (!hostAllowed(request, deps)) return redirect(deps, 'invalid_host');
  const url = new URL(request.url);
  if (url.searchParams.has('error')) return redirect(deps, 'error');
  const state = url.searchParams.get('state') || '';
  const code = url.searchParams.get('code') || '';
  if (!state || !code) return redirect(deps, 'invalid_request');
  const session = await deps.consumeOAuthSession(state);
  if (!session) return redirect(deps, 'invalid_state');
  try {
    const credential = await deps.exchangeAuthorizationCode(code, session);
    const account = await deps.createAccount({ accountId: (session as any).accountId, name: session.accountName, method: 'browser', credential });
    return redirect(deps, 'connected', account.id);
  } catch {
    return redirect(deps, 'error');
  }
}

export async function codexImportAuthCore(request: Request, deps: CodexRouteDeps) {
  const len = Number(request.headers.get('content-length') || 0);
  if (len > MAX_IMPORT_BYTES) return json({ error: 'body_too_large' }, 413);
  const body = await request.text();
  if (body.length > MAX_IMPORT_BYTES) return json({ error: 'body_too_large' }, 413);
  try {
    const credential = await deps.parseCodexAuthFile(body);
    const account = await deps.createAccount({ method: 'import', credential });
    return json({ account_id: account.id, revision: account.revision });
  } catch (error) {
    return json({ error: error instanceof Error ? error.message : 'invalid_auth_file' }, 400);
  }
}

export async function codexDeviceStartCore(_request: Request, deps: CodexRouteDeps) {
  return json(await deps.startDeviceFlow());
}

export async function codexDeviceStatusCore(_request: Request, session: string, deps: CodexRouteDeps) {
  const status = await deps.pollDeviceFlow(session);
  if (status.state === 'complete' && status.credential) {
    const account = await deps.createAccount({ method: 'device', credential: status.credential });
    return json({ state: 'complete' satisfies DeviceFlowState, account_id: account.id });
  }
  return json({ state: status.state, ...(status.interval ? { interval: status.interval } : {}) });
}
