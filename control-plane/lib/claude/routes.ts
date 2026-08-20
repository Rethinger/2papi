import { tx } from '../db';
import { consumeClaudeOAuthSession, exchangeClaudeCode, startClaudeOAuthSession } from './oauth';
import { createClaudeOAuthAccount } from './create-account';

const LOOPBACK_HOSTS = new Set(['localhost:54545', '127.0.0.1:54545']);

export type ClaudeRouteDeps = {
  dashboardPublicUrl: string;
  trustedProxy: boolean;
  startOAuthSession: () => Promise<{ authorization_url: string; state: string; expires_in: number }>;
  consumeOAuthSession: typeof consumeClaudeOAuthSession;
  exchangeAuthorizationCode: typeof exchangeClaudeCode;
  createAccount: (input: Parameters<typeof createClaudeOAuthAccount>[1]) => Promise<{ id: string; revision: number }>;
};

export function claudeRouteDeps(): ClaudeRouteDeps {
  return {
    dashboardPublicUrl: process.env.DASHBOARD_PUBLIC_URL || 'http://localhost:13000',
    trustedProxy: process.env.TRUSTED_PROXY === 'true',
    startOAuthSession: startClaudeOAuthSession,
    consumeOAuthSession: consumeClaudeOAuthSession,
    exchangeAuthorizationCode: exchangeClaudeCode,
    createAccount: input => tx(client => createClaudeOAuthAccount(client, input)),
  };
}

function json(body: unknown, status = 200) {
  return Response.json(body, { status });
}

function redirect(deps: ClaudeRouteDeps, status: string, accountId?: string) {
  const url = new URL(deps.dashboardPublicUrl);
  url.search = '';
  url.searchParams.set('claude_status', status);
  if (accountId) url.searchParams.set('account_id', accountId);
  return Response.redirect(url.toString(), 302);
}

function hostAllowed(request: Request, deps: ClaudeRouteDeps) {
  const directHost = request.headers.get('host') || new URL(request.url).host;
  if (!LOOPBACK_HOSTS.has(directHost)) return false;
  if (deps.trustedProxy) {
    const forwarded = request.headers.get('x-forwarded-host');
    if (forwarded && !LOOPBACK_HOSTS.has(forwarded)) return false;
  }
  return true;
}

export async function claudeOAuthStartCore(_request: Request, deps: ClaudeRouteDeps) {
  const started = await deps.startOAuthSession();
  return json({ authorization_url: started.authorization_url, state: started.state, expires_in: started.expires_in });
}

export async function claudeCallbackCore(request: Request, deps: ClaudeRouteDeps) {
  if (!hostAllowed(request, deps)) {
    return json({ error: 'forbidden', message: 'Callback must arrive on the local loopback host' }, 403);
  }
  const url = new URL(request.url);
  const code = url.searchParams.get('code');
  const state = url.searchParams.get('state');
  if (!code || !state) {
    return redirect(deps, 'error');
  }
  const session = await deps.consumeOAuthSession(state);
  if (!session) {
    return redirect(deps, 'error');
  }
  try {
    const token = await deps.exchangeAuthorizationCode(code, state, session);
    const account = await deps.createAccount({ token });
    return redirect(deps, 'connected', account.id);
  } catch {
    return redirect(deps, 'error');
  }
}
