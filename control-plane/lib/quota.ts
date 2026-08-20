import type { Pool, PoolClient } from 'pg';

// Quota aggregation for the dashboard Quota widget + /quota tab.
// Shape mirrors the gateway `/api/quota` so the frontend uses one contract.
//
// Sources:
//   - used tokens: request_events (24h rolling) per account (attempt position 0)
//   - adapter/kind: providers.adapter per account (kind derived, since the
//     encrypted secret record doesn't expose it)
//   - limit: not stored server-side → null. Live upstream percentages (codex
//     credits etc.) come from the gateway `/api/quota` in embedded/lite mode.

type Database = Pool | PoolClient;

export type ProviderQuota = {
  account: string;
  kind: string; // oauth|cookie|free|api_key (derived from adapter)
  family: string;
  adapter: string;
  used: number; // tokens, last 24h
  limit: number | null;
  percent: number; // 0 when no limit
  status: string; // active|disabled|unknown
  enabled: boolean;
};

export type QuotaResponse = {
  summary: { percent: number; used: number; limit: number | null; active: number };
  providers: ProviderQuota[];
};

const FAMILY: Record<string, string> = {
  'openai-codex': 'codex',
  'openai-compatible': 'openai',
  anthropic: 'claude',
  claudeai: 'claude',
  gemini: 'gemini',
  deepseek: 'deepseek',
  cursor: 'cursor',
  copilot: 'copilot',
  kimi: 'kimi',
  opencode: 'free',
  felo: 'free',
  qoder: 'free',
  free: 'free',
};

export function familyFor(adapter: string | null | undefined): string {
  const key = (adapter ?? '').toLowerCase();
  return FAMILY[key] ?? (key || 'unknown');
}

// kindFor derives a credential kind hint from the adapter for display.
export function kindFor(adapter: string | null | undefined): string {
  switch ((adapter ?? '').toLowerCase()) {
    case 'opencode':
    case 'felo':
    case 'qoder':
    case 'free':
      return 'free';
    case 'anthropic':
    case 'claudeai':
      return 'oauth';
    case 'openai-codex':
    case 'cursor':
    case 'copilot':
    case 'kimi':
      return 'oauth';
    default:
      return 'api_key';
  }
}

export async function summarizeQuota(database: Database): Promise<QuotaResponse> {
  // Per-account token usage from request_events (attempt position 0 = routed acct).
  const usageRows = await database.query(`
    SELECT a.account, count(DISTINCT e.id) AS requests,
           COALESCE(sum(e.input_tokens + e.output_tokens),0)::bigint AS tokens
    FROM request_events e
    JOIN request_event_attempts a ON a.request_event_id = e.id AND a.position = 0
    WHERE e.occurred_at >= now() - interval '24 hours'
    GROUP BY a.account`);
  const usage = new Map<string, { requests: number; tokens: number }>(
    (usageRows.rows as Array<{ account: string; requests: string; tokens: string }>).map(row => [
      row.account,
      { requests: Number(row.requests), tokens: Number(row.tokens) },
    ]),
  );

  const acctRows = await database.query(`
    SELECT a.name, a.enabled, p.adapter, p.name AS provider_name
    FROM accounts a JOIN providers p ON p.id = a.provider_id
    ORDER BY a.enabled DESC, a.priority, a.name`);

  const providers: ProviderQuota[] = (acctRows.rows as Array<{
    name: string;
    enabled: boolean;
    adapter: string | null;
    provider_name: string | null;
  }>).map(row => {
    const family = familyFor(row.adapter);
    const u = usage.get(row.name) ?? { requests: 0, tokens: 0 };
    const isFree = family === 'free';
    return {
      account: row.name,
      kind: kindFor(row.adapter),
      family,
      adapter: row.adapter ?? 'openai-compatible',
      used: u.tokens,
      limit: isFree ? null : null, // no provider limit stored; null = unknown
      percent: 0,
      status: row.enabled ? (u.requests > 0 ? 'active' : 'unknown') : 'disabled',
      enabled: row.enabled,
    };
  });

  const active = providers.filter(p => p.enabled).length;
  const used = providers.reduce((sum, p) => sum + p.used, 0);
  return {
    summary: { percent: 0, used, limit: null, active },
    providers,
  };
}
