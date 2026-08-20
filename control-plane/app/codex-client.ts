export const CODEX_AUTH_CHANNEL = '2papi-codex-auth';
export const MAX_AUTH_FILE_BYTES = 1024 * 1024;

export type CodexAuthMethod = 'browser' | 'device' | 'import';
export type DeviceState = 'pending' | 'slow_down' | 'expired' | 'denied' | 'failed' | 'complete';

export type DeviceStart = {
  session: string;
  user_code: string;
  verification_uri: string;
  expires_in: number;
  interval: number;
};

export type DeviceStatus = { state: DeviceState; interval?: number; account_id?: string };
export type DiscoveryScope =
  | { scope: 'all' }
  | { scope: 'provider_id'; provider_id: string }
  | { scope: 'account_id'; account_id: string };

export type DiscoveryResult = {
  account_id: string;
  account_name: string;
  status: 'succeeded' | 'failed';
  model_count?: number;
  warning_code?: string | null;
  error?: { code: string; message: string };
};

export type DiscoveredModel = {
  provider_id: string;
  provider_slug: string;
  provider_name: string;
  adapter: string;
  upstream_model: string;
  display_name: string;
  supported_in_api: boolean;
  account_count: number;
  available_account_count: number;
  accounts: Record<string, { available: boolean; display_name: string; last_seen_at: string }>;
  metadata: { context_window: number | null; tools: boolean | null; function_calling: boolean | null; reasoning: boolean | null; image_generation: boolean | null; supported_in_api: boolean | null; tier: string | null; owner: string | null; description: string | null; tool_names: string[] | null; input_modalities: string[] | null; parallel_tool_calls: boolean | null; last_seen_at: string | null };
};

export type CodexQuotaWindow = {
  used_percent: number;
  limit_window_seconds: number;
  reset_after_seconds: number;
  reset_at: number;
};

export type CodexQuotaState = {
  account_id: string;
  quota: {
    plan_type?: string;
    rate_limit?: {
      allowed: boolean;
      limit_reached: boolean;
      primary_window?: CodexQuotaWindow | null;
      secondary_window?: CodexQuotaWindow | null;
    };
    fetched_at?: string;
  };
  reset_credits: {
    available_count?: number;
    total_earned_count?: number;
    next_expires_at?: string;
    fetched_at?: string;
  };
  capability_status: 'unknown' | 'available' | 'unsupported' | 'contract_changed' | 'error';
  fetched_at?: string | null;
  last_error_code?: string | null;
  local_usage?: { tokens: number; requests: number; since: string } | null;
  reset_operation?: CodexResetOperation | null;
};

export type CodexResetOperation = {
  id: string;
  account_id: string;
  status: 'pending' | 'succeeded' | 'failed' | 'unknown';
  idempotency_key: string;
  warning_code?: string | null;
  resolution_source?: string | null;
  resolution_note?: string | null;
};

export class CodexClientError extends Error {
  constructor(public code: string, message: string, public status: number) {
    super(message);
  }
}

async function readPayload(response: Response) {
  return response.json().catch(() => ({})) as Promise<Record<string, unknown>>;
}

async function codexRequest<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`/api/control/v1/${path}`, init);
  const payload = await readPayload(response);
  if (!response.ok) {
    const structured = payload.error && typeof payload.error === 'object' ? payload.error as Record<string, unknown> : null;
    const code = String(structured?.code ?? payload.error ?? 'codex_request_failed');
    const message = String(structured?.message ?? code);
    throw new CodexClientError(code, message, response.status);
  }
  return (Object.prototype.hasOwnProperty.call(payload, 'data') ? payload.data : payload) as T;
}

const jsonHeaders = { 'Content-Type': 'application/json' };

export function startCodexBrowser(name?: string) {
  return codexRequest<{ authorization_url: string; expires_in: number }>('codex/oauth/start', {
    method: 'POST', headers: jsonHeaders, body: JSON.stringify({ name: name || undefined }),
  });
}

export function reauthorizeCodexAccount(accountId: string) {
  return codexRequest<{ authorization_url: string; expires_in: number }>(`accounts/${accountId}/reauthorize`, {
    method: 'POST', headers: jsonHeaders, body: '{}',
  });
}

export function startCodexDevice() {
  return codexRequest<DeviceStart>('codex/device/start', { method: 'POST', headers: jsonHeaders, body: '{}' });
}

export function getCodexDeviceStatus(session: string) {
  return codexRequest<DeviceStatus>(`codex/device/${encodeURIComponent(session)}/status`);
}

export function importCodexAuth(contents: string) {
  return codexRequest<{ account_id: string; revision: number }>('codex/import-auth', {
    method: 'POST', headers: jsonHeaders, body: contents,
  });
}

export function patchAccount(accountId: string, changes: Record<string, unknown>) {
  return codexRequest<Record<string, unknown>>(`accounts/${accountId}`, {
    method: 'PATCH', headers: jsonHeaders, body: JSON.stringify(changes),
  });
}

export function discoverCodexModels(scope: DiscoveryScope) {
  return codexRequest<{ scope: string; results: DiscoveryResult[] }>('model-discovery', {
    method: 'POST', headers: jsonHeaders, body: JSON.stringify(scope),
  });
}

export function getDiscoveredModels() {
  return codexRequest<DiscoveredModel[]>('discovered-models');
}

export type DiscoveredModelStrategy = 'round_robin' | 'quota_failover' | 'p2c' | 'least_used' | 'lkgp' | 'reset_aware';

export function importDiscoveredModel(input: { alias: string; provider_id: string; upstream_model: string; routing_strategy: DiscoveredModelStrategy }) {
  return codexRequest<Record<string, unknown>>('models/import-selection', {
    method: 'POST', headers: jsonHeaders, body: JSON.stringify(input),
  });
}

export function getCodexQuota(accountId: string) {
  return codexRequest<CodexQuotaState>(`accounts/${accountId}/quota`);
}

export function refreshCodexQuota(accountId: string) {
  return codexRequest<CodexQuotaState>(`accounts/${accountId}/quota/refresh`, {
    method: 'POST', headers: jsonHeaders, body: '{}',
  });
}

export function resetCodexQuota(accountId: string, idempotencyKey: string) {
  return codexRequest<CodexResetOperation>(`accounts/${accountId}/quota/reset`, {
    method: 'POST',
    headers: { ...jsonHeaders, 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify({ confirmed: true }),
  });
}

export function reconcileCodexQuotaReset(accountId: string, operationId: string) {
  return codexRequest<CodexResetOperation>(`accounts/${accountId}/quota/reset/${operationId}/reconcile`, {
    method: 'POST', headers: jsonHeaders, body: '{}',
  });
}

export function resolveCodexQuotaReset(accountId: string, operationId: string, resolution: 'succeeded' | 'failed', note: string) {
  return codexRequest<CodexResetOperation>(`accounts/${accountId}/quota/reset/${operationId}/resolve`, {
    method: 'POST', headers: jsonHeaders, body: JSON.stringify({ resolution, note }),
  });
}

export const PUBLIC_ALIAS_RE = /^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$/;

export function isValidPublicAlias(alias: string) {
  return PUBLIC_ALIAS_RE.test(alias) && !/[._:/-]$/.test(alias) && !/\s|[\x00-\x1f\x7f]/.test(alias);
}

export function codexPlanLabel(plan: unknown, fallback: string) {
  if (typeof plan !== 'string' || !plan.trim()) return fallback;
  const known: Record<string, string> = { free: 'Free', plus: 'Plus', pro: 'Pro', team: 'Team', business: 'Business', enterprise: 'Enterprise' };
  return known[plan.toLowerCase()] ?? plan;
}

export function clampQuotaPercent(value: unknown) {
  const number = typeof value === 'number' && Number.isFinite(value) ? value : 0;
  return Math.min(100, Math.max(0, number));
}

export function splitRemaining(ms: number) {
  const total = Math.max(0, Math.floor(ms / 1000));
  return { hours: Math.floor(total / 3600), minutes: Math.floor((total % 3600) / 60), seconds: total % 60 };
}

export function openCodexAuthWindow(url: string) {
  const popup = window.open('', '2papi-codex-auth', 'popup,width=560,height=760');
  if (!popup) return false;
  popup.opener = null;
  popup.location.href = url;
  popup.focus();
  return true;
}
