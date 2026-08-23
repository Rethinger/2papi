'use client';

import {
  AlertFillIcon,
  CheckCircleFillIcon,
  CopyIcon,
  CpuIcon,
  CreditCardIcon,
  DatabaseIcon,
  GearIcon,
  GitBranchIcon,
  GlobeIcon,
  HistoryIcon,
  HomeIcon,
  KeyIcon,
  OrganizationIcon,
  PlusIcon,
  PencilIcon,
  PulseIcon,
  RocketIcon,
  SearchIcon,
  ServerIcon,
  ShieldLockIcon,
  StackIcon,
  SyncIcon,
  TrashIcon,
  ZapIcon,
} from '@primer/octicons-react';
import { FormEvent, useCallback, useEffect, useMemo, useState, useTransition } from 'react';
import {
  createTranslator,
  dateLocale,
  isLocale,
  localeCookieName,
  localeStorageKey,
  type Locale,
  type MessageKey,
  type Translator,
} from './i18n';
import { CODEX_AUTH_CHANNEL, MAX_AUTH_FILE_BYTES, discoverCodexModels } from './codex-client';
import { CodexAccountModal } from './components/codex-account-modal';
import { ClaudeAccountModal, CLAUDE_AUTH_CHANNEL } from './components/claude-account-modal';
import { CodexAccountCard } from './components/codex-account-card';
import { CommandPalette } from './components/command-palette';
import { ModelDiscoveryModal } from './components/model-discovery-modal';
import { ModelCard, type ProviderStrategy } from './components/model-card';
import { accountDefaultsForProvider, type AccountDraft } from './account-form';
import { pathForView, viewFromPath, type View } from './view-router';
import { BillingView } from './billing-view';
import { McpServersView } from './mcp-view';

type RequestAttemptRow = {
  account: string;
  adapter: string;
  status: number;
  outcome: string;
  latency_ms: number;
  cooldown_ms: number;
};

type RequestEventRow = {
  id: string;
  request_id: string;
  occurred_at: string;
  endpoint: string;
  public_model: string;
  upstream_model: string;
  virtual_key: string;
  streaming: boolean;
  config_version: number;
  final_status: number;
  success: boolean;
  total_latency_ms: number;
  total_tokens: number;
  attempts: RequestAttemptRow[];
};

type ResourceMap = {
  overview: Record<string, number | null>;
  providers: any[];
  accounts: any[];
  models: any[];
  keys: any[];
  teams: any[];
  versions: any[];
  audit: any[];
  requests: RequestEventRow[];
  routing: any;
  webhook: any;
  trends: any[];
  proxyPool: { raw?: string } | null;
  billing: { checkout_url?: string; configured?: boolean; balances: any[]; transactions: any[] } | null;
  mcpServers: any[];
};

type UiError = { detail: string; fallback: MessageKey } | null;
type EditingResource = { kind: 'provider' | 'account' | 'model' | 'key' | 'team'; item: any } | null;
type DeletingResource = { kind: 'provider' | 'account' | 'model'; item: any } | null;

const emptyData: ResourceMap = {
  overview: {},
  providers: [],
  accounts: [],
  models: [],
  keys: [],
  teams: [],
  versions: [],
  audit: [],
  requests: [],
  routing: null,
  webhook: { enabled: false, url: '', secret: '' },
  trends: [],
  proxyPool: null,
  billing: null,
  mcpServers: [],
};

const nav = [
  ['overview', 'nav.overview', HomeIcon],
  ['requests', 'nav.requests', PulseIcon],
  ['accounts', 'nav.accounts', ServerIcon],
  ['models', 'nav.models', GitBranchIcon],
  ['keys', 'nav.keys', KeyIcon],
  ['teams', 'nav.teams', OrganizationIcon],
  ['billing', 'nav.billing', CreditCardIcon],
  ['mcp', 'nav.mcp', StackIcon],
  ['audit', 'nav.audit', HistoryIcon],
] as const satisfies ReadonlyArray<readonly [View, MessageKey, typeof HomeIcon]>;

async function api(path: string, init?: RequestInit) {
  const response = await fetch(`/api/control/v1/${path}`, {
    ...init,
    headers: { 'Content-Type': 'application/json', ...(init?.headers ?? {}) },
  });
  const payload = await response.json();
  if (!response.ok) throw new Error(payload?.error?.message ?? '');
  return payload.data;
}

function uiError(cause: unknown, fallback: MessageKey): Exclude<UiError, null> {
  return { detail: cause instanceof Error ? cause.message : '', fallback };
}

function persistLocale(locale: Locale) {
  document.cookie = `${localeCookieName}=${locale}; Path=/; Max-Age=31536000; SameSite=Lax`;
  document.documentElement.lang = locale;
  try {
    window.localStorage.setItem(localeStorageKey, locale);
  } catch {
    // The cookie still preserves the explicit choice when local storage is unavailable.
  }
}

function Status({ good = true, children }: { good?: boolean; children: React.ReactNode }) {
  return (
    <span className={`status ${good ? 'good' : 'warn'}`}>
      {good ? <CheckCircleFillIcon size={12} /> : <AlertFillIcon size={12} />}
      {children}
    </span>
  );
}

function Metric({ label, value, detail }: { label: string; value: string | number; detail: string }) {
  return (
    <article className="metric-card">
      <span className="metric-label">{label}</span>
      <strong>{value}</strong>
      <span className="metric-detail">{detail}</span>
    </article>
  );
}

function LocaleSwitch({ locale, onChange, t, mobile = false }: {
  locale: Locale;
  onChange: (locale: Locale) => void;
  t: Translator;
  mobile?: boolean;
}) {
  return (
    <div className={`locale-switch ${mobile ? 'mobile-locale' : ''}`} role="group" aria-label={t('locale.label')}>
      {(['ru', 'en'] as const).map(option => (
        <button
          key={option}
          type="button"
          className={locale === option ? 'active' : ''}
          aria-pressed={locale === option}
          onClick={() => onChange(option)}
        >
          {option.toUpperCase()}
        </button>
      ))}
    </div>
  );
}

function Modal({ title, children, onClose, t }: {
  title: string;
  children: React.ReactNode;
  onClose: () => void;
  t: Translator;
}) {
  useEffect(() => {
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', closeOnEscape);
    return () => window.removeEventListener('keydown', closeOnEscape);
  }, [onClose]);

  return (
    <div className="modal-backdrop" role="presentation" onMouseDown={onClose}>
      <section
        className="modal"
        role="dialog"
        aria-modal="true"
        aria-label={title}
        onMouseDown={event => event.stopPropagation()}
      >
        <header>
          <div><span className="eyebrow">{t('modal.eyebrow')}</span><h2>{title}</h2></div>
          <button className="icon-button" onClick={onClose} aria-label={t('modal.close')}>×</button>
        </header>
        {children}
      </section>
    </div>
  );
}

function modelAccountCount(count: number, locale: Locale, t: Translator) {
  if (locale === 'ru') {
    const last = count % 10;
    const lastTwo = count % 100;
    if (last === 1 && lastTwo !== 11) return t('models.accountCountOne', { count });
    if (last >= 2 && last <= 4 && (lastTwo < 12 || lastTwo > 14)) return t('models.accountCountFew', { count });
  }
  return t(count === 1 ? 'models.accountCountOne' : 'models.accountCountMany', { count });
}

function configStatusLabel(status: string | null | undefined, t: Translator): string {
  if (status === 'draft') return t('status.config.draft');
  if (status === 'published') return t('status.config.published');
  if (status === 'rolled_back') return t('status.config.rolledBack');
  return status ?? '';
}

function requestOutcomeLabel(outcome: string, t: Translator): string {
  if (outcome === 'success') return t('requests.outcome.success');
  if (outcome === 'rate_limited') return t('requests.outcome.rateLimited');
  if (outcome === 'upstream_error') return t('requests.outcome.upstreamError');
  if (outcome === 'saturated') return t('requests.outcome.saturated');
  if (outcome === 'canceled') return t('requests.outcome.canceled');
  return t('requests.outcome.rejected');
}

export default function DashboardClient({ initialLocale }: { initialLocale: Locale }) {
  const [locale, setLocale] = useState<Locale>(initialLocale);
  const [view, setView] = useState<View>(() => (typeof window === 'undefined' ? 'overview' : viewFromPath(window.location.pathname)));
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null);
  const [data, setData] = useState<ResourceMap>(emptyData);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<UiError>(null);
  const [modal, setModal] = useState<'provider' | 'account' | 'claude' | 'codex' | 'discovery' | 'model' | 'key' | 'team' | 'routing' | 'batch' | 'batch-models' | null>(null);
  const [providerTab, setProviderTab] = useState<string | null>(null);
  const [batchProviderId, setBatchProviderId] = useState('');
  const [batchCodexFiles, setBatchCodexFiles] = useState<File[]>([]);
  const [discoveryAccountId, setDiscoveryAccountId] = useState<string | undefined>();
  const [rotating, setRotating] = useState<any>(null);
  const [syncingModelId, setSyncingModelId] = useState<string | null>(null);
  const [editing, setEditing] = useState<EditingResource>(null);
  const [deleting, setDeleting] = useState<DeletingResource>(null);
  const [accountDraft, setAccountDraft] = useState<AccountDraft>({ providerId: '', baseURL: '', adapter: '', credentialKind: 'api_key' });
  const [proxyPoolSave, setProxyPoolSave] = useState<{ proxy_count?: number } | null>(null);
  const [createdKey, setCreatedKey] = useState('');
  const [isPending, startTransition] = useTransition();
  const t = useMemo(() => createTranslator(locale), [locale]);

  useEffect(() => {
    document.documentElement.lang = initialLocale;
    try {
      const stored = window.localStorage.getItem(localeStorageKey);
      if (!isLocale(stored) || stored !== initialLocale) window.localStorage.setItem(localeStorageKey, initialLocale);
    } catch {
      // Storage can be unavailable in hardened browsers. The server-selected locale remains valid.
    }
  }, [initialLocale]);

  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const claudeStatus = params.get('claude_status');
    const codexStatus = params.get('codex_status');
    const status = claudeStatus ?? codexStatus;
    if (!status) return;
    const accountId = params.get('account_id') ?? undefined;
    const channel = new BroadcastChannel(claudeStatus ? CLAUDE_AUTH_CHANNEL : CODEX_AUTH_CHANNEL);
    channel.postMessage({ status, accountId });
    channel.close();
    params.delete('codex_status');
    params.delete('claude_status');
    params.delete('account_id');
    const query = params.toString();
    window.history.replaceState({}, '', `${window.location.pathname}${query ? `?${query}` : ''}${window.location.hash}`);
    if (window.name === '2papi-codex-auth' || window.name === '2papi-claude-auth') window.setTimeout(() => window.close(), 250);
  }, []);

  const selectLocale = useCallback((nextLocale: Locale) => {
    setLocale(nextLocale);
    persistLocale(nextLocale);
  }, []);

  // Silent polls refresh only the live-changing data; slowly changing
  // resources (providers, keys, versions, audit, routing) load on mount and
  // on the manual Refresh button.
  const LIVE_PATHS = ['overview', 'accounts', 'models', 'request-events?limit=100', 'teams'] as const;

  const load = useCallback(async (options: { silent?: boolean } = {}) => {
    const silent = Boolean(options.silent);
    setError(null);
    if (!silent) setLoading(true);
    try {
      if (silent) {
        const [overview, accounts, models, requests, teams, billing, mcpServers] = await Promise.all(LIVE_PATHS.map(path => api(path)).concat([api('billing'), api('mcp-servers')]));
        setData(previous => ({ ...previous, overview, accounts, models, requests, teams, billing, mcpServers }));
        setLastUpdated(new Date());
      } else {
        const [overview, providers, accounts, models, keys, teams, versions, audit, requests, routing, webhook, trends, proxyPool, billing, mcpServers] = await Promise.all([
          api('overview'),
          api('providers'),
          api('accounts'),
          api('models'),
          api('virtual-keys'),
          api('teams'),
          api('config-versions'),
          api('audit-events'),
          api('request-events?limit=100'),
          api('routing'),
          api('webhook'),
          api('request-trends?days=14'),
          api('proxy-pool'),
          api('billing'),
          api('mcp-servers'),
        ]);
        setData({ overview, providers, accounts, models, keys, teams, versions, audit, requests, routing, webhook, trends, proxyPool, billing, mcpServers });
        setLastUpdated(new Date());
      }
    } catch (cause) {
      setError(uiError(cause, 'error.controlUnavailable'));
    } finally {
      if (!silent) setLoading(false);
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  // Keep the dashboard live: silent refresh while the tab is visible, plus an
  // immediate refresh when it becomes visible again. Never flashes the loading layer.
  useEffect(() => {
    if (typeof window === 'undefined') return;
    const reload = () => { if (document.visibilityState !== 'hidden') void load({ silent: true }); };
    const onVisible = () => { if (document.visibilityState === 'visible') void load({ silent: true }); };
    const id = window.setInterval(reload, 60_000);
    document.addEventListener('visibilitychange', onVisible);
    return () => {
      window.clearInterval(id);
      document.removeEventListener('visibilitychange', onVisible);
    };
  }, [load]);

  useEffect(() => {
    const channel = new BroadcastChannel(CODEX_AUTH_CHANNEL);
    channel.onmessage = event => {
      if ((event.data as { status?: string }).status === 'connected') void load();
    };
    return () => channel.close();
  }, [load]);

  useEffect(() => {
    const channel = new BroadcastChannel(CLAUDE_AUTH_CHANNEL);
    channel.onmessage = event => {
      if ((event.data as { status?: string }).status === 'connected') void load();
    };
    return () => channel.close();
  }, [load]);

  const latestVersion = data.versions[0];
  const enabledAccounts = data.accounts.filter(account => account.enabled);
  const providerById = useMemo(
    () => new Map(data.providers.map(provider => [provider.id, provider])),
    [data.providers],
  );
  const codexProviderIds = useMemo(
    () => new Set(data.providers.filter(provider => provider.adapter === 'openai-codex').map(provider => provider.id)),
    [data.providers],
  );
  const claudeProviderIds = useMemo(
    () => new Set(data.providers.filter(provider => provider.adapter === 'anthropic').map(provider => provider.id)),
    [data.providers],
  );
  const codexAccounts = useMemo(
    () => data.accounts.filter(account => codexProviderIds.has(account.provider_id)),
    [codexProviderIds, data.accounts],
  );
  const claudeAccounts = useMemo(
    () => data.accounts.filter(account => claudeProviderIds.has(account.provider_id)),
    [claudeProviderIds, data.accounts],
  );
  const standardAccounts = useMemo(
    () => data.accounts.filter(account => !codexProviderIds.has(account.provider_id) && !claudeProviderIds.has(account.provider_id)),
    [codexProviderIds, claudeProviderIds, data.accounts],
  );
  const currentNavLabel = t(nav.find(item => item[0] === view)?.[1] ?? 'nav.overview');

  // URL routing: every view has a real path so tabs are deep-linkable and
  // the browser back/forward buttons work. History API only — no page reloads.
  const navigate = useCallback((next: View) => {
    setView(next);
    if (typeof window !== 'undefined') {
      const path = pathForView(next);
      if (window.location.pathname !== path) {
        window.history.pushState({ view: next }, '', path);
      }
      window.scrollTo({ top: 0 });
    }
  }, []);

  useEffect(() => {
    const onPop = () => setView(viewFromPath(window.location.pathname));
    window.addEventListener('popstate', onPop);
    return () => window.removeEventListener('popstate', onPop);
  }, []);

  useEffect(() => {
    const label = nav.find(item => item[0] === view)?.[1] ?? 'nav.overview';
    document.title = `2papi · ${t(label)}`;
  }, [view, t]);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault();
        setPaletteOpen(open => !open);
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, []);

  function mutate(work: () => Promise<unknown>) {
    startTransition(async () => {
      setError(null);
      try {
        await work();
        setModal(null);
        await load();
      } catch (cause) {
        setError(uiError(cause, 'error.actionFailed'));
      }
    });
  }

  function submitProvider(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    mutate(() => api('providers', {
      method: 'POST',
      body: JSON.stringify({
        slug: form.get('slug'),
        name: form.get('name'),
        adapter: form.get('adapter'),
        base_url: form.get('base_url'),
        enabled: true,
        metadata: {},
      }),
    }));
  }

  function submitAccount(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const kind = accountDraft.credentialKind;
    let credential: Record<string, unknown>;
    if (kind === 'cookie') {
      credential = { kind, cookies: form.get('cookies'), organization_id: form.get('organization_id') || undefined };
    } else if (kind === 'oauth') {
      credential = { kind, access_token: form.get('access_token'), organization_id: form.get('organization_id') || undefined };
    } else {
      credential = { kind, api_key: form.get('api_key') };
    }
    mutate(() => api('accounts', {
      method: 'POST',
      body: JSON.stringify({
        provider_id: form.get('provider_id'),
        name: form.get('name'),
        display_name: form.get('display_name'),
        base_url: accountDraft.baseURL,
        enabled: true,
        priority: Number(form.get('priority') || 1),
        weight: 1,
        max_concurrency: Number(form.get('max_concurrency') || 100),
        cost: 0,
        credential,
        metadata: {},
        proxy: String(form.get('proxy') || ''),
      }),
    }));
  }

  function submitProxyPool(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    startTransition(async () => {
      setError(null);
      try {
        const result = await api('proxy-pool', { method: 'POST', body: JSON.stringify({ raw: String(form.get('proxy_pool_raw') || '') }) });
        setProxyPoolSave(result);
        await load();
      } catch (cause) {
        setError(uiError(cause, 'error.proxyPoolFailed'));
      }
    });
  }

  function submitModel(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    mutate(() => api('models', {
      method: 'POST',
      body: JSON.stringify({
        alias: form.get('alias'),
        upstream_model: form.get('upstream_model'),
        enabled: true,
        accounts: form.getAll('accounts'),
        fallbacks: form.getAll('fallbacks'),
        input_per_mtok: Number(form.get('input_per_mtok') || 0),
        output_per_mtok: Number(form.get('output_per_mtok') || 0),
      }),
    }));
  }

  function submitKey(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    startTransition(async () => {
      setError(null);
      try {
        const result = await api('virtual-keys', {
          method: 'POST',
          body: JSON.stringify({
            name: form.get('name'),
            models: form.getAll('models'),
            rpm: Number(form.get('rpm') || 60),
            tpm: Number(form.get('tpm') || 0),
            max_concurrency: Number(form.get('max_concurrency') || 0),
            budget_usd: Number(form.get('budget_usd') || 0),
            team_id: form.get('team_id') || null,
            enabled: true,
          }),
        });
        setCreatedKey(result.plaintext_key);
        setModal(null);
        await load();
      } catch (cause) {
        setError(uiError(cause, 'error.keyCreationFailed'));
      }
    });
  }

  function submitTeam(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    mutate(() => api('teams', {
      method: 'POST',
      body: JSON.stringify({
        name: form.get('name'),
        budget_usd: Number(form.get('budget_usd') || 0),
        enabled: true,
      }),
    }));
  }

  function submitBatchAccounts(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const providerId = String(form.get('provider_id') || batchProviderId);
    const entries: Array<{ name?: string; secret: string }> = [];
    for (const line of String(form.get('entries') ?? '').split('\n')) {
      const trimmed = line.trim();
      if (!trimmed || trimmed.startsWith('#')) continue;
      const colon = trimmed.indexOf(':');
      if (colon > 0) {
        entries.push({ name: trimmed.slice(0, colon).trim(), secret: trimmed.slice(colon + 1).trim() });
      } else {
        entries.push({ secret: trimmed });
      }
    }
    if (!entries.length) return;
    mutate(() => api('accounts/batch', {
      method: 'POST',
      body: JSON.stringify({
        provider_id: providerId,
        kind: form.get('kind') || 'api_key',
        entries,
        priority: Number(form.get('priority') || 1),
        max_concurrency: Number(form.get('max_concurrency') || 100),
      }),
    }));
  }

  // submitBatchCodex imports many Codex credentials at once: each pasted line
  // is either a full auth.json document, a "name:token" pair, or a bare
  // access token; selected .json files are appended as auth.json documents.
  function submitBatchCodex(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const providerId = String(form.get('provider_id') || batchProviderId);
    if (!providerId) return;
    const entries: Array<{ name?: string; raw: string }> = [];
    for (const line of String(form.get('entries') ?? '').split('\n')) {
      const trimmed = line.trim();
      if (!trimmed || trimmed.startsWith('#')) continue;
      if (trimmed.startsWith('{')) {
        entries.push({ raw: trimmed });
        continue;
      }
      const colon = trimmed.indexOf(':');
      if (colon > 0 && !trimmed.startsWith('eyJ')) {
        entries.push({ name: trimmed.slice(0, colon).trim(), raw: trimmed.slice(colon + 1).trim() });
      } else {
        entries.push({ raw: trimmed });
      }
    }
    mutate(async () => {
      for (const file of batchCodexFiles) {
        if (file.size > MAX_AUTH_FILE_BYTES) throw new Error('codex file too large');
        entries.push({ raw: await file.text() });
      }
      if (!entries.length) return;
      await api('codex/import-auth-batch', {
        method: 'POST',
        body: JSON.stringify({
          provider_id: providerId,
          entries,
          max_concurrency: Number(form.get('max_concurrency') || 1),
        }),
      });
    });
  }

  function submitBatchModels(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const lines = String(form.get('lines') ?? '').split('\n').map(line => line.trim()).filter(Boolean);
    if (!lines.length) return;
    mutate(() => api('models/batch', {
      method: 'POST',
      body: JSON.stringify({
        lines,
        accounts: form.getAll('accounts'),
        fallbacks: form.getAll('fallbacks'),
        input_per_mtok: Number(form.get('input_per_mtok') || 0),
        output_per_mtok: Number(form.get('output_per_mtok') || 0),
      }),
    }));
  }

  function submitWebhook(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    mutate(() => api('webhook', {
      method: 'POST',
      body: JSON.stringify({
        enabled: form.get('enabled') === 'on',
        url: form.get('url'),
        secret: form.get('secret'),
      }),
    }));
  }

  function exportSnapshot() {
    startTransition(async () => {
      try {
        const snapshot = await api('export');
        const blob = new Blob([JSON.stringify(snapshot, null, 2)], { type: 'application/json' });
        const url = URL.createObjectURL(blob);
        const anchor = document.createElement('a');
        anchor.href = url;
        anchor.download = `2papi-snapshot-${new Date().toISOString().slice(0, 10)}.json`;
        anchor.click();
        URL.revokeObjectURL(url);
      } catch (cause) {
        setError(uiError(cause, 'error.actionFailed'));
      }
    });
  }

  function importSnapshotFile(file?: File) {
    if (!file) return;
    startTransition(async () => {
      try {
        const snapshot = JSON.parse(await file.text());
        await api('import', { method: 'POST', body: JSON.stringify({ snapshot }) });
        setModal(null);
        await load();
      } catch (cause) {
        setError(uiError(cause, 'error.actionFailed'));
      }
    });
  }

  function submitRouting(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    mutate(() => api('routing', {
      method: 'POST',
      body: JSON.stringify({
        strategy: form.get('strategy'),
        sticky_ttl: form.get('sticky_ttl'),
        max_attempts: Number(form.get('max_attempts') || 2),
        resilience: {
          cooldown: form.get('cooldown'),
          circuit_failures: Number(form.get('circuit_failures') || 3),
          circuit_reset: form.get('circuit_reset'),
          lockout_failures: Number(form.get('lockout_failures') || 10),
          lockout_duration: form.get('lockout_duration'),
        },
        optimization: { rtk_compression: form.get('rtk_compression') === 'on', caveman: form.get('caveman') === 'on', headroom: form.get('headroom') === 'on', headroom_reserve: Number(form.get('headroom_reserve') || 120000), headroom_keep: Number(form.get('headroom_keep') || 8) },
      }),
    }));
  }

  function toggleOptimization(flag: 'rtk_compression' | 'caveman' | 'headroom') {
    const enabled = !(data.routing?.optimization?.[flag] ?? false);
    mutate(() => api('routing', {
      method: 'POST',
      body: JSON.stringify({
        strategy: data.routing?.strategy ?? 'balanced',
        sticky_ttl: data.routing?.sticky_ttl ?? '1h',
        max_attempts: Number(data.routing?.max_attempts ?? 2),
        resilience: {
          cooldown: data.routing?.resilience?.cooldown ?? '30s',
          circuit_failures: Number(data.routing?.resilience?.circuit_failures ?? 3),
          circuit_reset: data.routing?.resilience?.circuit_reset ?? '1m',
          lockout_failures: Number(data.routing?.resilience?.lockout_failures ?? 10),
          lockout_duration: data.routing?.resilience?.lockout_duration ?? '15m',
        },
        optimization: {
          rtk_compression: flag === 'rtk_compression' ? enabled : Boolean(data.routing?.optimization?.rtk_compression),
          caveman: flag === 'caveman' ? enabled : Boolean(data.routing?.optimization?.caveman),
          headroom: flag === 'headroom' ? enabled : Boolean(data.routing?.optimization?.headroom),
          headroom_reserve: Number(data.routing?.optimization?.headroom_reserve) || 120000,
          headroom_keep: Number(data.routing?.optimization?.headroom_keep) || 8,
        },
      }),
    }));
  }

  function submitRotation(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const account = rotating;
    startTransition(async () => {
      setError(null);
      try {
        await api(`accounts/${account.id}`, {
          method: 'PATCH',
          body: JSON.stringify({ credential: { api_key: form.get('api_key') } }),
        });
        setRotating(null);
        await load();
      } catch (cause) {
        setError(uiError(cause, 'error.rotationFailed'));
      }
    });
  }

  function publish() {
    mutate(() => api('config-versions/publish', { method: 'POST', body: '{}' }));
  }

  function rollback(version: number) {
    mutate(async () => {
      await api('config-versions/rollback', { method: 'POST', body: JSON.stringify({ version }) });
      await api('config-versions/publish', { method: 'POST', body: '{}' });
    });
  }

  function toggleAccount(account: any) {
    mutate(() => api(`accounts/${account.id}`, {
      method: 'PATCH',
      body: JSON.stringify({ enabled: !account.enabled }),
    }));
  }

  function toggleResource(resource: 'providers' | 'models' | 'virtual-keys' | 'teams', item: any) {
    mutate(() => api(`${resource}/${item.id}`, {
      method: 'PATCH',
      body: JSON.stringify({ enabled: !item.enabled }),
    }));
  }

  function openAccountModal() {
    setAccountDraft({ providerId: '', baseURL: '', adapter: '', credentialKind: 'api_key' });
    setModal('account');
  }

  function deleteResource() {
    if (!deleting) return;
    const resource = deleting.kind === 'provider' ? 'providers' : deleting.kind === 'model' ? 'models' : 'accounts';
    startTransition(async () => {
      setError(null);
      try {
        await api(`${resource}/${deleting.item.id}`, { method: 'DELETE' });
        setDeleting(null);
        await load();
      } catch (cause) { setError(uiError(cause, 'error.actionFailed')); }
    });
  }

  function updateModelStrategy(model: any, routing_strategy: ProviderStrategy) {
    mutate(() => api(`models/${model.id}`, { method: 'PATCH', body: JSON.stringify({ routing_strategy }) }));
  }

  async function syncModelMetadata(model: any) {
    if (!model.provider_id) return;
    setSyncingModelId(model.id);
    setError(null);
    try {
      await discoverCodexModels({ scope: 'provider_id', provider_id: model.provider_id });
      await load({ silent: true });
    } catch (cause) {
      setError(uiError(cause, 'error.controlUnavailable'));
    } finally {
      setSyncingModelId(null);
    }
  }

  function submitEditing(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!editing) return;
    const form = new FormData(event.currentTarget);
    const routes = { provider: 'providers', account: 'accounts', model: 'models', key: 'virtual-keys', team: 'teams' } as const;
    let body: Record<string, unknown>;
    switch (editing.kind) {
      case 'provider':
        body = { name: form.get('name'), base_url: form.get('base_url') };
        break;
      case 'team':
        body = { name: form.get('name'), budget_usd: Number(form.get('budget_usd') || 0) };
        break;
      case 'account':
        body = {
          display_name: form.get('display_name'), base_url: form.get('base_url'),
          priority: Number(form.get('priority')),
          max_concurrency: Number(form.get('max_concurrency')),
          proxy: String(form.get('proxy') || ''),
        };
        break;
      case 'model':
        body = {
          alias: form.get('alias'), upstream_model: form.get('upstream_model'),
          fallbacks: form.getAll('fallbacks'),
          input_per_mtok: Number(form.get('input_per_mtok') || 0),
          output_per_mtok: Number(form.get('output_per_mtok') || 0),
          ...(!editing.item.provider_id ? { accounts: form.getAll('accounts') } : {}),
        };
        break;
      case 'key':
        body = {
          name: form.get('name'), rpm: Number(form.get('rpm')), models: form.getAll('models'),
          tpm: Number(form.get('tpm') || 0),
          max_concurrency: Number(form.get('max_concurrency') || 0),
          budget_usd: Number(form.get('budget_usd') || 0),
          team_id: form.get('team_id') || null,
        };
        break;
    }
    startTransition(async () => {
      setError(null);
      try {
        await api(`${routes[editing.kind]}/${editing.item.id}`, { method: 'PATCH', body: JSON.stringify(body) });
        setEditing(null);
        await load();
      } catch (cause) { setError(uiError(cause, 'error.actionFailed')); }
    });
  }

  return (
    <div className="app-shell">
      <aside className="sidebar">
        <div className="brand">
          <span className="brand-mark"><PulseIcon size={16} /></span>
          <div><b>2papi</b><small>CONTROL PLANE</small></div>
        </div>
        <nav aria-label={t('nav.label')}>
          {nav.map(([id, labelKey, Icon]) => (
            <button key={id} className={view === id ? 'active' : ''} onClick={() => navigate(id)}>
              <Icon size={16} />
              <span>{t(labelKey)}</span>
              {id === 'accounts' && <em>{data.accounts.length}</em>}
            </button>
          ))}
        </nav>
        <div className="sidebar-foot">
          <LocaleSwitch locale={locale} onChange={selectLocale} t={t} />
          <div className="system-line">
            <span className="live-dot" />
            <div><b>{t('system.gatewayOnline')}</b><small>localhost:18080</small></div>
          </div>
          <div className="build-label">{t('system.build')}</div>
        </div>
      </aside>

      <main className="workspace">
        <header className="topbar">
          <div className="topbar-left">
            <span className="breadcrumb">2papi / {currentNavLabel}</span>
            <span className="status-hint">
              <span className="live-dot" />
              {lastUpdated ? t('app.lastUpdated', { time: lastUpdated.toLocaleTimeString() }) : t('app.autoRefresh')}
            </span>
          </div>
          <div className="top-actions">
            <LocaleSwitch locale={locale} onChange={selectLocale} t={t} mobile />
            <button className="ghost palette-trigger" onClick={() => setPaletteOpen(true)}>
              <SearchIcon size={13} /><kbd>Ctrl K</kbd>
            </button>
            <button className="secondary" onClick={() => void load()} disabled={loading}>
              <SyncIcon size={16} />{t('action.refresh')}
            </button>
            <button className="publish" onClick={publish} disabled={isPending}>
              <RocketIcon size={16} />{t('action.publish')}
            </button>
          </div>
        </header>

        {error && (
          <div className="alert">
            <AlertFillIcon size={16} />
            <div><b>{t('alert.title')}</b><span>{error.detail || t(error.fallback)}</span></div>
            <button onClick={() => setError(null)} aria-label={t('action.dismiss')}>×</button>
          </div>
        )}
        {createdKey && (
          <div className="secret-banner">
            <ShieldLockIcon size={16} />
            <div><b>{t('secret.notice')}</b><code>{createdKey}</code></div>
            <button onClick={() => navigator.clipboard.writeText(createdKey)}>
              <CopyIcon size={16} />{t('action.copy')}
            </button>
            <button className="icon-button" onClick={() => setCreatedKey('')} aria-label={t('action.dismiss')}>×</button>
          </div>
        )}

        {view === 'overview' && (
          <section className="page">
            <div className="page-heading">
              <div>
                <span className="eyebrow">{t('overview.eyebrow')}</span>
                <h1>{t('overview.title')}</h1>
                <p>{t('overview.description')}</p>
              </div>
              <button className="primary" onClick={openAccountModal}>
                <PlusIcon size={16} />{t('action.connectAccount')}
              </button>
            </div>
            <div className="metrics">
              <Metric label={t('overview.metric.activeAccounts')} value={enabledAccounts.length} detail={t('overview.metric.configured', { count: data.accounts.length })} />
              <Metric label={t('overview.metric.publicModels')} value={data.models.length} detail={t('overview.metric.providers', { count: data.overview.providers ?? 0 })} />
              <Metric label={t('overview.metric.virtualKeys')} value={data.keys.length} detail={t('overview.metric.policyProtected')} />
              <Metric label={t('overview.metric.activeSnapshot')} value={latestVersion?.version ? `v${latestVersion.version}` : 'YAML'} detail={latestVersion?.status ? configStatusLabel(latestVersion.status, t) : t('overview.metric.fallbackRuntime')} />
            </div>
            {data.trends.length > 0 && (
              <article className="panel trend-panel">
                <div className="panel-heading"><div><span className="eyebrow">{t('trends.eyebrow')}</span><h2>{t('trends.title')}</h2></div><small>{t('trends.subtitle')}</small></div>
                <div className="trend-chart">
                  {(() => {
                    const max = Math.max(...data.trends.map(day => Number(day.tokens ?? 0)), 1);
                    return data.trends.map(day => {
                      const height = Math.max(5, Math.round(Number(day.tokens ?? 0) / max * 100));
                      return (
                        <div className="trend-bar" key={day.day} title={`${day.day}: ${day.requests} req · ${Number(day.tokens ?? 0).toLocaleString(dateLocale(locale))} tok · ${day.success_rate}%`}>
                          <div className="trend-fill" style={{ height: `${height}%` }} />
                          <span>{String(day.day).slice(5)}</span>
                        </div>
                      );
                    });
                  })()}
                </div>
              </article>
            )}
            <div className="metrics operations-metrics">
              <Metric label={t('overview.metric.requests24h')} value={data.overview.requests_24h ?? 0} detail={t('overview.metric.last24h')} />
              <Metric label={t('overview.metric.successRate')} value={`${Math.round(Number(data.overview.success_rate_24h ?? 0) * 100)}%`} detail={t('overview.metric.last24h')} />
              <Metric label={t('overview.metric.p95Latency')} value={`${Math.round(Number(data.overview.p95_latency_ms_24h ?? 0))} ms`} detail={t('overview.metric.last24h')} />
              <Metric label={t('overview.metric.tokens24h')} value={Number(data.overview.tokens_24h ?? 0).toLocaleString(dateLocale(locale))} detail={t('overview.metric.last24h')} />
            </div>
            <div className="overview-grid">
              <article className="panel account-panel">
                <div className="panel-heading">
                  <div><span className="eyebrow">{t('overview.pool.eyebrow')}</span><h2>{t('overview.pool.title')}</h2></div>
                  <button className="ghost" onClick={() => navigate('accounts')}>{t('action.manageAll')}</button>
                </div>
                <div className="account-grid">
                  {data.accounts.slice(0, 4).map((account, index) => {
                    const capacity = 72 - index * 9;
                    return (
                      <div className="account-card" key={account.id}>
                        <div className="account-title">
                          <span className={`provider-glyph tone-${index % 4}`}><CpuIcon size={16} /></span>
                          <div><b>{account.display_name}</b><small>{providerById.get(account.provider_id)?.name ?? account.name}</small></div>
                          <Status good={account.enabled}>{account.enabled ? t('overview.pool.ready') : t('overview.pool.disabled')}</Status>
                        </div>
                        <div className="account-meta">
                          <span>{t('overview.pool.priority')} <b>{account.priority}</b></span>
                          <span>{t('overview.pool.limit')} <b>{account.max_concurrency}</b></span>
                        </div>
                        <div className="quota"><span style={{ width: `${capacity}%` }} /></div>
                        <small className="quota-copy">{t('overview.pool.capacity', { percent: capacity })}</small>
                      </div>
                    );
                  })}
                  {!data.accounts.length && (
                    <button className="empty-card" onClick={openAccountModal}>
                      <PlusIcon size={24} /><b>{t('overview.pool.emptyTitle')}</b><span>{t('overview.pool.emptyBody')}</span>
                    </button>
                  )}
                </div>
              </article>

              <article className="panel route-panel">
                <div className="panel-heading">
                  <div><span className="eyebrow">{t('overview.snapshot.eyebrow')}</span><h2>{t('overview.snapshot.title')}</h2></div>
                  <Status good={!error}>{error ? t('overview.snapshot.degraded') : t('overview.snapshot.inSync')}</Status>
                </div>
                <div className="route-rail">
                  <div className="rail-node"><DatabaseIcon size={16} /><div><b>PostgreSQL</b><span>{t('overview.snapshot.desiredState')}</span></div></div>
                  <i />
                  <div className="rail-node"><StackIcon size={16} /><div><b>Snapshot {latestVersion?.version ? `v${latestVersion.version}` : t('overview.snapshot.fallback')}</b><span>{latestVersion?.status ? configStatusLabel(latestVersion.status, t) : t('overview.snapshot.localYaml')}</span></div></div>
                  <i />
                  <div className="rail-node active"><ServerIcon size={16} /><div><b>Gateway</b><span>{t('overview.snapshot.serving')}</span></div></div>
                </div>
                <div className="version-list">
                  {data.versions.slice(0, 4).map((version, index) => (
                    <div key={version.version}>
                      <span>v{version.version}</span>
                      <code>{String(version.checksum).slice(0, 10)}</code>
                      <Status good={version.status === 'published'}>{configStatusLabel(version.status, t)}</Status>
                      {index > 0 && version.status === 'published' && (
                        <button className="ghost" onClick={() => rollback(version.version)} disabled={isPending}>
                          <HistoryIcon size={14} />{t('action.rollback')}
                        </button>
                      )}
                    </div>
                  ))}
                  {!data.versions.length && <p>{t('overview.snapshot.empty')}</p>}
                </div>
              </article>
            </div>
          </section>
        )}

        {view === 'requests' && (
          <ResourcePage
            eyebrow={t('requests.eyebrow')}
            title={t('requests.title')}
            description={t('requests.description')}
            action={<button className="secondary" onClick={() => void load()}><SyncIcon size={16} />{t('action.refresh')}</button>}
          >
            <div className="metrics request-metrics">
              <Metric label={t('overview.metric.requests24h')} value={data.overview.requests_24h ?? 0} detail={t('overview.metric.last24h')} />
              <Metric label={t('overview.metric.successRate')} value={`${Math.round(Number(data.overview.success_rate_24h ?? 0) * 100)}%`} detail={t('overview.metric.last24h')} />
              <Metric label={t('overview.metric.p95Latency')} value={`${Math.round(Number(data.overview.p95_latency_ms_24h ?? 0))} ms`} detail={t('overview.metric.last24h')} />
              <Metric label={t('overview.metric.tokens24h')} value={Number(data.overview.tokens_24h ?? 0).toLocaleString(dateLocale(locale))} detail={t('overview.metric.last24h')} />
            </div>
            <div className="request-list">
              {data.requests.map(event => (
                <details className="request-row" key={event.id}>
                  <summary>
                    <span className={`request-state ${event.success ? 'success' : 'failure'}`} />
                    <div className="request-identity">
                      <code title={event.request_id}>{event.request_id}</code>
                      <time>{new Date(event.occurred_at).toLocaleString(dateLocale(locale))}</time>
                    </div>
                    <div className="request-model">
                      <b>{event.public_model}</b>
                      <small>{event.upstream_model || event.endpoint}{event.streaming ? ` · ${t('requests.streaming')}` : ''}</small>
                    </div>
                    <div className="request-route">
                      <b>{event.attempts.map(attempt => attempt.account).join(' → ') || '—'}</b>
                      <small>{event.virtual_key || '—'} · v{event.config_version}</small>
                    </div>
                    <Status good={event.success}>HTTP {event.final_status}</Status>
                    <div className="request-numbers">
                      <b>{event.total_latency_ms} ms</b>
                      <small>{t('requests.tokens', { count: event.total_tokens })}</small>
                    </div>
                  </summary>
                  <div className="attempt-list">
                    <span className="eyebrow">{t('requests.attempts')}</span>
                    {event.attempts.map((attempt, position) => (
                      <div className="attempt-row" key={`${event.id}-${position}`}>
                        <span>{position + 1}</span>
                        <div><b>{attempt.account}</b><small>{attempt.adapter}</small></div>
                        <Status good={attempt.outcome === 'success'}>{requestOutcomeLabel(attempt.outcome, t)}</Status>
                        <code>HTTP {attempt.status}</code>
                        <span>{attempt.latency_ms} ms</span>
                      </div>
                    ))}
                  </div>
                </details>
              ))}
              {!data.requests.length && <div className="empty-inline">{t('requests.empty')}</div>}
            </div>
          </ResourcePage>
        )}

        {view === 'accounts' && (
          <ResourcePage
            eyebrow={t('accounts.eyebrow')}
            title={t('accounts.title')}
            description={t('accounts.description')}
            action={<>
              <button className="primary claude-action" onClick={() => setModal('claude')}><PlusIcon size={16} />{t('action.addClaudeAccount')}</button>
              <button className="primary codex-action" onClick={() => setModal('codex')}><PlusIcon size={16} />{t('action.addCodexAccount')}</button>
              <button className="secondary" onClick={() => setModal('provider')}><PlusIcon size={16} />{t('action.addProvider')}</button>
              <button className="secondary" onClick={openAccountModal}><PlusIcon size={16} />{t('action.addAccount')}</button>
            </>}
          >
            <div className="provider-tabs" role="tablist">
              <button className={providerTab === null ? 'active' : ''} onClick={() => setProviderTab(null)}>{t('accounts.tab.all')}</button>
              {data.providers.map(provider => (
                <button key={provider.id} className={providerTab === provider.id ? 'active' : ''} onClick={() => setProviderTab(provider.id)}>{provider.name}</button>
              ))}
            </div>
            {providerTab && (() => {
              const provider = data.providers.find(item => item.id === providerTab);
              if (!provider) return null;
              const accounts = data.accounts.filter(account => account.provider_id === providerTab);
              const addAccountLabel = provider.adapter === 'openai-codex'
                ? 'action.addCodexAccount'
                : provider.adapter === 'anthropic'
                  ? 'action.addClaudeAccount'
                  : 'action.addAccount';
              return (<>
              <div className="section-heading provider-tab-heading">
                <div><h2>{provider.name}</h2><p>{provider.slug} · {provider.adapter} · {t('accounts.tab.accountCount', { count: accounts.length })}</p></div>
                <div className="heading-actions">
                  <button className="secondary" onClick={() => { setBatchCodexFiles([]); setBatchProviderId(providerTab); setModal('batch'); }}><StackIcon size={14} />{t('action.batchImport')}</button>
                  <button className="primary" onClick={() => setModal(provider.adapter === 'openai-codex' ? 'codex' : provider.adapter === 'anthropic' ? 'claude' : 'account')}><PlusIcon size={14} />{t(addAccountLabel as MessageKey)}</button>
                </div>
              </div>
              {provider.adapter === 'openai-codex' && (
                <div className="codex-account-grid">
                  {accounts.map(account => (
                    <CodexAccountCard
                      key={account.id}
                      account={account}
                      locale={locale}
                      t={t}
                      onChanged={load}
                      onDiscover={() => { setDiscoveryAccountId(account.id); setModal('discovery'); }}
                      onToggle={() => toggleAccount(account)}
                      onEdit={() => setEditing({ kind: 'account', item: account })}
                      onDelete={() => setDeleting({ kind: 'account', item: account })}
                      onError={cause => setError(uiError(cause, 'error.codexActionFailed'))}
                    />
                  ))}
                  {!accounts.length && <button className="empty-state codex-empty" onClick={() => setModal('codex')}><PlusIcon size={24} /><b>{t('action.addCodexAccount')}</b><span>{t('codex.modal.secureBody')}</span></button>}
                </div>
              )}
              {provider.adapter === 'anthropic' && (
                <div className="codex-account-grid">
                  {accounts.map(account => (
                    <ClaudeAccountMiniCard
                      key={account.id}
                      account={account}
                      t={t}
                      isPending={isPending}
                      onEdit={() => setEditing({ kind: 'account', item: account })}
                      onToggle={() => toggleAccount(account)}
                      onDelete={() => setDeleting({ kind: 'account', item: account })}
                    />
                  ))}
                  {!accounts.length && <button className="empty-state codex-empty" onClick={() => setModal('claude')}><PlusIcon size={24} /><b>{t('action.addClaudeAccount')}</b><span>{t('claude.modal.secureBody')}</span></button>}
                </div>
              )}
              {provider.adapter !== 'openai-codex' && provider.adapter !== 'anthropic' && (
                <StandardAccountTable
                  accounts={accounts}
                  providerById={providerById}
                  t={t}
                  isPending={isPending}
                  onDiscover={account => { setDiscoveryAccountId(account.id); setModal('discovery'); }}
                  onRotate={setRotating}
                  onEdit={account => setEditing({ kind: 'account', item: account })}
                  onToggle={toggleAccount}
                  onDelete={account => setDeleting({ kind: 'account', item: account })}
                  onAdd={() => setModal('account')}
                />
              )}
              </>);
            })()}
            {!providerTab && (<>
            <div className="section-heading">
              <div><h2>{t('accounts.providersTitle')}</h2><p>{t('accounts.providersDescription')}</p></div>
            </div>
            <div className="table-card provider-table">
              <table>
                <thead><tr><th>{t('accounts.column.provider')}</th><th>{t('accounts.column.endpoint')}</th><th>{t('accounts.column.status')}</th><th /></tr></thead>
                <tbody>{data.providers.map(provider => <tr key={provider.id}>
                  <td><div className="cell-title"><span className="provider-glyph"><CpuIcon size={16} /></span><div><b>{provider.name}</b><small>{provider.slug} · {provider.adapter}</small></div></div></td>
                  <td><code className="truncate">{provider.base_url}</code></td>
                  <td><Status good={provider.enabled}>{provider.enabled ? t('accounts.enabled') : t('accounts.disabled')}</Status></td>
                  <td><div className="row-actions"><button className="ghost" onClick={() => { setBatchCodexFiles([]); setBatchProviderId(provider.id); setModal('batch'); }}><StackIcon size={13} />{t('action.batchImport')}</button><button className="ghost" onClick={() => setEditing({ kind: 'provider', item: provider })}><PencilIcon size={13} />{t('action.edit')}</button><button className="ghost" onClick={() => toggleResource('providers', provider)}>{provider.enabled ? t('action.disable') : t('action.enable')}</button><button className="danger-quiet" onClick={() => setDeleting({ kind: 'provider', item: provider })}><TrashIcon size={13} />{t('action.delete')}</button></div></td>
                </tr>)}</tbody>
              </table>
            </div>
            <div className="section-heading">
              <div><h2>{t('accounts.claudeTitle')}</h2><p>{t('accounts.claudeDescription')}</p></div>
              <div className="heading-actions">
                <button className="ghost" onClick={() => { setBatchCodexFiles([]); setBatchProviderId(data.providers.filter(item => item.adapter === 'anthropic').length === 1 ? data.providers.find(item => item.adapter === 'anthropic')!.id : ''); setModal('batch'); }}><StackIcon size={14} />{t('action.batchImport')}</button>
                <button className="ghost" onClick={() => setModal('claude')}><PlusIcon size={14} />{t('action.addClaudeAccount')}</button>
              </div>
            </div>
            <div className="codex-account-grid">
              {claudeAccounts.map(account => (
                <ClaudeAccountMiniCard
                  key={account.id}
                  account={account}
                  t={t}
                  isPending={isPending}
                  onEdit={() => setEditing({ kind: 'account', item: account })}
                  onToggle={() => toggleAccount(account)}
                  onDelete={() => setDeleting({ kind: 'account', item: account })}
                />
              ))}
              {!claudeAccounts.length && <button className="empty-state codex-empty" onClick={() => setModal('claude')}><PlusIcon size={24} /><b>{t('action.addClaudeAccount')}</b><span>{t('claude.modal.secureBody')}</span></button>}
            </div>
            <div className="section-heading">
              <div><h2>{t('accounts.codexTitle')}</h2><p>{t('accounts.codexDescription')}</p></div>
              <div className="heading-actions">
                <button className="ghost" onClick={() => { setBatchCodexFiles([]); setBatchProviderId(data.providers.filter(item => item.adapter === 'openai-codex').length === 1 ? data.providers.find(item => item.adapter === 'openai-codex')!.id : ''); setModal('batch'); }}><StackIcon size={14} />{t('action.batchImport')}</button>
                <button className="ghost" onClick={() => { setDiscoveryAccountId(undefined); setModal('discovery'); }} disabled={!codexAccounts.length}><SyncIcon size={14} />{t('action.discoverModels')}</button>
              </div>
            </div>
            <div className="codex-account-grid">
              {codexAccounts.map(account => (
                <CodexAccountCard
                  key={account.id}
                  account={account}
                  locale={locale}
                  t={t}
                  onChanged={load}
                  onDiscover={() => { setDiscoveryAccountId(account.id); setModal('discovery'); }}
                  onToggle={() => toggleAccount(account)}
                  onEdit={() => setEditing({ kind: 'account', item: account })}
                  onDelete={() => setDeleting({ kind: 'account', item: account })}
                  onError={cause => setError(uiError(cause, 'error.codexActionFailed'))}
                />
              ))}
              {!codexAccounts.length && <button className="empty-state codex-empty" onClick={() => setModal('codex')}><PlusIcon size={24} /><b>{t('action.addCodexAccount')}</b><span>{t('codex.modal.secureBody')}</span></button>}
            </div>
            <div className="section-heading standard-heading"><div><h2>{t('accounts.standardTitle')}</h2></div></div>
            <StandardAccountTable
              accounts={standardAccounts}
              providerById={providerById}
              t={t}
              isPending={isPending}
              onDiscover={account => { setDiscoveryAccountId(account.id); setModal('discovery'); }}
              onRotate={setRotating}
              onEdit={account => setEditing({ kind: 'account', item: account })}
              onToggle={toggleAccount}
              onDelete={account => setDeleting({ kind: 'account', item: account })}
              onAdd={openAccountModal}
            />
            </>)}
          </ResourcePage>
        )}

        {view === 'models' && (
          <ResourcePage
            eyebrow={t('models.eyebrow')}
            title={t('models.title')}
            description={t('models.description')}
            action={<>
              <button className="secondary" onClick={() => { setDiscoveryAccountId(undefined); setModal('discovery'); }}><SyncIcon size={16} />{t('action.discoverModels')}</button>
              <button className="secondary" onClick={() => setModal('batch-models')}><StackIcon size={16} />{t('action.batchImportModels')}</button>
              <button className="primary" onClick={() => setModal('model')}><PlusIcon size={16} />{t('action.newModelRoute')}</button>
            </>}
          >
            <div className="model-grid">
              {data.models.map(model => <ModelCard key={model.id} model={model} locale={locale} t={t} onEdit={() => setEditing({ kind: 'model', item: model })} onToggle={() => toggleResource('models', model)} onDelete={() => setDeleting({ kind: 'model', item: model })} onStrategy={strategy => updateModelStrategy(model, strategy)} onSync={() => void syncModelMetadata(model)} syncing={syncingModelId === model.id} />)}
              {!data.models.length && <Empty title={t('models.emptyTitle')} body={t('models.emptyBody')} onClick={() => setModal('model')} />}
            </div>
          </ResourcePage>
        )}

        {view === 'keys' && (
          <ResourcePage
            eyebrow={t('keys.eyebrow')}
            title={t('keys.title')}
            description={t('keys.description')}
            action={<button className="primary" onClick={() => setModal('key')}><PlusIcon size={16} />{t('action.createApiKey')}</button>}
          >
            <div className="table-card">
              <table>
                <thead><tr><th>{t('keys.column.name')}</th><th>{t('keys.column.prefix')}</th><th>{t('keys.column.models')}</th><th>{t('keys.column.rpm')}</th><th>{t('keys.column.team')}</th><th>{t('keys.column.spend')}</th><th>{t('keys.column.created')}</th><th>{t('keys.column.status')}</th><th /></tr></thead>
                <tbody>
                  {data.keys.map(key => (
                    <tr key={key.id}>
                      <td><div className="cell-title"><span className="provider-glyph tone-3"><KeyIcon size={16} /></span><b>{key.name}</b>{key.budget_usd > 0 && <small>{t('keys.budget', { usd: Number(key.budget_usd) })}</small>}</div></td>
                      <td><code>{key.key_prefix}…</code></td>
                      <td>{key.models?.length ? key.models.join(', ') : t('keys.allModels')}</td>
                      <td>{key.rpm}</td>
                      <td>{key.team_name ?? '—'}</td>
                      <td>${Number(key.spend_today ?? 0).toFixed(4)}</td>
                      <td>{new Date(key.created_at).toLocaleDateString(dateLocale(locale))}</td>
                      <td><Status good={key.enabled}>{key.enabled ? t('keys.active') : t('keys.disabled')}</Status></td>
                      <td><div className="row-actions"><button className="ghost" onClick={() => setEditing({ kind: 'key', item: key })}><PencilIcon size={13} />{t('action.edit')}</button><button className="ghost" onClick={() => toggleResource('virtual-keys', key)}>{key.enabled ? t('action.disable') : t('action.enable')}</button></div></td>
                    </tr>
                  ))}
                </tbody>
              </table>
              {!data.keys.length && <Empty title={t('keys.emptyTitle')} body={t('keys.emptyBody')} onClick={() => setModal('key')} />}
            </div>
          </ResourcePage>
        )}

        {view === 'teams' && (
          <ResourcePage
            eyebrow={t('teams.eyebrow')}
            title={t('teams.title')}
            description={t('teams.description')}
            action={<button className="primary" onClick={() => setModal('team')}><PlusIcon size={16} />{t('action.createTeam')}</button>}
          >
            <div className="table-card">
              <table>
                <thead><tr><th>{t('teams.column.name')}</th><th>{t('teams.column.budget')}</th><th>{t('teams.column.share')}</th><th>{t('teams.column.spend')}</th><th>{t('teams.column.status')}</th><th /></tr></thead>
                <tbody>
                  {data.teams.map(team => (
                    <tr key={team.id}>
                      <td><div className="cell-title"><span className="provider-glyph tone-2"><OrganizationIcon size={16} /></span><b>{team.name}</b><small>{t('teams.keyCount', { count: team.key_count ?? 0 })}</small></div></td>
                      <td>${Number(team.budget_usd ?? 0).toFixed(4)}/day</td>
                      <td>{Number(team.share_usd ?? 0) > 0 ? `$${Number(team.share_usd).toFixed(4)}/key` : '—'}</td>
                      <td>${Number(team.spend_today ?? 0).toFixed(4)}</td>
                      <td><Status good={team.enabled}>{team.enabled ? t('teams.active') : t('teams.disabled')}</Status></td>
                      <td><div className="row-actions"><button className="ghost" onClick={() => setEditing({ kind: 'team', item: team })}><PencilIcon size={13} />{t('action.edit')}</button><button className="ghost" onClick={() => toggleResource('teams', team)}>{team.enabled ? t('action.disable') : t('action.enable')}</button></div></td>
                    </tr>
                  ))}
                </tbody>
              </table>
              {!data.teams.length && <Empty title={t('teams.emptyTitle')} body={t('teams.emptyBody')} onClick={() => setModal('team')} />}
            </div>
          </ResourcePage>
        )}

        {view === 'billing' && (
          <BillingView
            billing={data.billing}
            locale={locale}
            t={t as unknown as (key: string, vars?: Record<string, unknown>) => string}
            onAdjusted={() => void load({ silent: true })}
          />
        )}

        {view === 'mcp' && (
          <McpServersView
            mcpServers={data.mcpServers}
            locale={locale}
            t={t as unknown as (key: string, vars?: Record<string, unknown>) => string}
            onDone={() => void load({ silent: true })}
          />
        )}

        {view === 'audit' && (
          <ResourcePage
            eyebrow={t('audit.eyebrow')}
            title={t('audit.title')}
            description={t('audit.description')}
            action={<button className="secondary" onClick={() => void load()}><SyncIcon size={16} />{t('action.refresh')}</button>}
          >
            <div className="timeline">
              {data.audit.map(event => (
                <article key={event.id}>
                  <span className="timeline-dot" />
                  <div>
                    <div className="timeline-title"><b>{event.action}</b><span>{event.resource_type}</span><time>{new Date(event.created_at).toLocaleString(dateLocale(locale))}</time></div>
                    <code>{event.resource_id || t('audit.system')}</code>
                  </div>
                </article>
              ))}
              {!data.audit.length && <div className="empty-inline">{t('audit.empty')}</div>}
            </div>
          </ResourcePage>
        )}

        {view === 'settings' && (
          <ResourcePage
            eyebrow={t('settings.eyebrow')}
            title={t('settings.title')}
            description={t('settings.description')}
            action={<button className="primary" onClick={() => setModal('routing')}><GearIcon size={16} />{t('action.editRouting')}</button>}
          >
            <div className="settings-grid">
              <article className="panel"><GitBranchIcon size={24} /><h3>{t('settings.routing.title')}</h3><p>{t('settings.routing.description', { strategy: data.routing?.strategy ?? 'balanced', ttl: data.routing?.sticky_ttl ?? '1h', attempts: data.routing?.max_attempts ?? 2 })}</p><Status>{t('settings.routing.status')}</Status></article>
              <article className="panel"><ShieldLockIcon size={24} /><h3>{t('settings.secrets.title')}</h3><p>{t('settings.secrets.description')}</p><Status>{t('settings.secrets.status')}</Status></article>
              <article className="panel"><DatabaseIcon size={24} /><h3>{t('settings.persistence.title')}</h3><p>{t('settings.persistence.description')}</p><Status>{t('settings.persistence.status')}</Status></article>
              <article className="panel"><ServerIcon size={24} /><h3>{t('settings.fallback.title')}</h3><p>{t('settings.fallback.description')}</p><Status>{t('settings.fallback.status')}</Status></article>
              <article className="panel"><ZapIcon size={24} /><h3>{t('settings.optimization.title')}</h3><p>{t('settings.optimization.description')}</p>
                <div className="panel-rows">
                  <div className="panel-actions"><div><b>{t('settings.optimization.rtkTitle')}</b><small>{t('settings.optimization.rtkDescription')}</small></div><button className="secondary" onClick={() => toggleOptimization('rtk_compression')}>{t(data.routing?.optimization?.rtk_compression ? 'action.optimization.off' : 'action.optimization.on')}</button></div>
                  <div className="panel-actions"><div><b>{t('settings.optimization.headroomTitle')}</b><small>{t('settings.optimization.headroomDescription')}</small></div><button className="secondary" onClick={() => toggleOptimization('headroom')}>{t(data.routing?.optimization?.headroom ? 'action.optimization.off' : 'action.optimization.on')}</button></div>
                  <div className="panel-actions"><div><b>{t('settings.optimization.cavemanTitle')}</b><small>{t('settings.optimization.cavemanDescription')}</small></div><button className="secondary" onClick={() => toggleOptimization('caveman')}>{t(data.routing?.optimization?.caveman ? 'action.optimization.off' : 'action.optimization.on')}</button></div>
                  <Status good={data.routing?.optimization?.rtk_compression || data.routing?.optimization?.caveman || data.routing?.optimization?.headroom}>{t(data.routing?.optimization?.rtk_compression || data.routing?.optimization?.caveman || data.routing?.optimization?.headroom ? 'settings.optimization.status.on' : 'settings.optimization.status.off')}</Status>
                </div>
              </article>
              <article className="panel webhook-panel"><PulseIcon size={24} /><h3>{t('settings.webhook.title')}</h3><p>{t('settings.webhook.description')}</p>
                <form className="settings-form" onSubmit={submitWebhook}>
                  <label className="check-row"><input type="checkbox" name="enabled" defaultChecked={data.webhook?.enabled ?? false} /><span>{t('settings.webhook.enabled')}</span></label>
                  <Field label={t('settings.webhook.url')} name="url" type="url" defaultValue={data.webhook?.url ?? ''} placeholder="https://example.com/hook" />
                  <Field label={t('settings.webhook.secret')} name="secret" type="password" defaultValue={data.webhook?.secret ?? ''} placeholder={t('settings.webhook.secretPlaceholder')} />
                  <button className="secondary" type="submit" disabled={isPending}>{t('action.save')}</button>
                </form>
              </article>
              <article className="panel proxy-pool-panel"><GlobeIcon size={24} /><h3>{t('settings.proxyPool.title')}</h3><p>{t('settings.proxyPool.description')}</p>
                <form className="settings-form" onSubmit={submitProxyPool}>
                  <label>{t('settings.proxyPool.raw')}<textarea name="proxy_pool_raw" rows={8} defaultValue={data.proxyPool?.raw ?? ''} placeholder={'http://user:pass@host:8080\nsocks5://host:1080\nhost:3128'} spellCheck={false} /></label>
                  <small className="field-hint">{t('settings.proxyPool.hint')}</small>
                  {proxyPoolSave && (
                    <div className="inline-state">
                      <Status good={proxyPoolSave.proxy_count !== undefined && proxyPoolSave.proxy_count > 0}>{t('settings.proxyPool.saved', { count: proxyPoolSave.proxy_count ?? 0 })}</Status>
                    </div>
                  )}
                  <button className="secondary" type="submit" disabled={isPending}>{t('action.save')}</button>
                </form>
              </article>
              <article className="panel backup-panel"><HistoryIcon size={24} /><h3>{t('settings.backup.title')}</h3><p>{t('settings.backup.description')}</p>
                <div className="panel-actions">
                  <button className="secondary" onClick={exportSnapshot} disabled={isPending}>{t('settings.backup.export')}</button>
                  <label className="secondary file-button">{t('settings.backup.import')}<input type="file" accept=".json,application/json" onChange={event => importSnapshotFile(event.target.files?.[0] ?? undefined)} /></label>
                </div>
              </article>
            </div>
          </ResourcePage>
        )}


        {loading && <div className="loading-layer"><span /><b>{t('loading.controlPlane')}</b></div>}
      </main>

      {modal === 'claude' && (
        <ClaudeAccountModal
          t={t}
          onClose={() => setModal(null)}
          onConnected={async () => { setModal(null); await load(); }}
          onError={cause => setError(uiError(cause, 'error.codexActionFailed'))}
        />
      )}
      {modal === 'codex' && (
        <CodexAccountModal
          t={t}
          onClose={() => setModal(null)}
          onConnected={async () => { setModal(null); await load(); }}
          onError={cause => setError(uiError(cause, 'error.codexActionFailed'))}
        />
      )}
      {modal === 'discovery' && (
        <ModelDiscoveryModal
          accounts={data.accounts}
          providers={data.providers}
          existingAliases={data.models.map(model => model.alias)}
          initialAccountId={discoveryAccountId}
          t={t}
          onClose={() => { setModal(null); setDiscoveryAccountId(undefined); }}
          onImported={load}
          onError={cause => setError(uiError(cause, 'error.codexActionFailed'))}
        />
      )}
      {modal === 'provider' && (
        <Modal title={t('modal.provider.title')} onClose={() => setModal(null)} t={t}>
          <form onSubmit={submitProvider}>
            <Field label={t('form.displayName')} name="name" placeholder="Claude" />
            <Field label={t('form.slug')} name="slug" placeholder="claude" />
            <label>{t('form.adapter')}
              <select name="adapter" defaultValue="openai-compatible">
                {(['openai-compatible', 'anthropic', 'gemini'] as const).map(adapter => <option key={adapter} value={adapter}>{adapter}</option>)}
              </select>
            </label>
            <Field label={t('form.baseUrl')} name="base_url" placeholder="https://api.anthropic.com" type="url" />
            <FormActions pending={isPending} t={t} />
          </form>
        </Modal>
      )}
      {modal === 'account' && (
        <Modal title={t('modal.account.title')} onClose={() => setModal(null)} t={t}>
          <form onSubmit={submitAccount}>
            <label>{t('form.provider')}
              <select name="provider_id" required value={accountDraft.providerId} onChange={event => setAccountDraft(accountDefaultsForProvider(data.providers, event.target.value))}>
                <option value="" disabled>{t('form.selectProvider')}</option>
                {data.providers.map(provider => <option key={provider.id} value={provider.id}>{provider.name}</option>)}
              </select>
            </label>
            <div className="form-row">
              <Field label={t('form.internalName')} name="name" placeholder="claude-primary" />
              <Field label={t('form.displayName')} name="display_name" placeholder="Claude Primary" />
            </div>
            <label>{t('form.baseUrl')}<input name="base_url" type="url" value={accountDraft.baseURL} readOnly required /></label>
            {accountDraft.adapter === 'anthropic' ? (
              <>
                <fieldset><legend>{t('form.credentialMethod')}</legend>
                  <div className="auth-tabs" role="tablist">
                    {(['api_key', 'cookie', 'oauth'] as const).map(method => (
                      <button key={method} type="button" role="tab" aria-selected={accountDraft.credentialKind === method} className={accountDraft.credentialKind === method ? 'active' : ''} onClick={() => setAccountDraft(draft => ({ ...draft, credentialKind: method }))}>
                        {t(({ api_key: 'credential.kind.apiKey', cookie: 'credential.kind.cookie', oauth: 'credential.kind.oauth' } as const)[method])}
                      </button>
                    ))}
                  </div>
                </fieldset>
                {accountDraft.credentialKind === 'api_key' && <Field label={t('form.apiKey')} name="api_key" placeholder="sk-ant-…" type="password" />}
                {accountDraft.credentialKind === 'cookie' && (
                  <>
                    <label>{t('form.cookies')}<textarea name="cookies" rows={3} placeholder="sessionKey=sk-ant-…" required /></label>
                    <Field label={t('form.organizationId')} name="organization_id" placeholder={t('form.organizationOptional')} />
                  </>
                )}
                {accountDraft.credentialKind === 'oauth' && (
                  <>
                    <Field label={t('form.accessToken')} name="access_token" placeholder="sk-ant-…" type="password" />
                    <Field label={t('form.organizationId')} name="organization_id" placeholder={t('form.organizationOptional')} />
                  </>
                )}
              </>
            ) : (
              <Field label={t('form.apiKey')} name="api_key" placeholder="sk-…" type="password" />
            )}
            <div className="form-row">
              <Field label={t('form.priority')} name="priority" type="number" defaultValue="1" />
              <Field label={t('form.concurrency')} name="max_concurrency" type="number" defaultValue="100" />
            </div>
            <label>{t('form.proxy')}<textarea name="proxy" rows={2} placeholder="http://user:pass@host:8080" spellCheck={false} /><small className="field-hint">{t('form.proxyHint')}</small></label>
            <FormActions pending={isPending} t={t} />
          </form>
        </Modal>
      )}
      {modal === 'model' && (
        <Modal title={t('modal.model.title')} onClose={() => setModal(null)} t={t}>
          <form onSubmit={submitModel}>
            <div className="form-row">
              <Field label={t('form.publicAlias')} name="alias" placeholder="gpt-fast" />
              <Field label={t('form.upstreamModel')} name="upstream_model" placeholder="gpt-4o-mini" />
            </div>
            <fieldset><legend>{t('form.eligibleAccounts')}</legend>{data.accounts.map(account => <label className="check-row" key={account.id}><input type="checkbox" name="accounts" value={account.id} /><span>{account.display_name}</span><small>{account.name}</small></label>)}</fieldset>
            <fieldset><legend>{t('form.pricing')}</legend><div className="form-row">
              <Field label={t('form.inputPerMtok')} name="input_per_mtok" type="number" step="any" defaultValue="0" />
              <Field label={t('form.outputPerMtok')} name="output_per_mtok" type="number" step="any" defaultValue="0" />
            </div></fieldset>
            <fieldset><legend>{t('form.fallbacks')}</legend>{data.models.map(model => <label className="check-row" key={model.id}><input type="checkbox" name="fallbacks" value={model.alias} /><span>{model.alias}</span><small>{model.upstream_model}</small></label>)}<small className="field-hint">{t('form.fallbackHint')}</small></fieldset>
            <FormActions pending={isPending} t={t} />
          </form>
        </Modal>
      )}
      {modal === 'key' && (
        <Modal title={t('modal.key.title')} onClose={() => setModal(null)} t={t}>
          <form onSubmit={submitKey}>
            <Field label={t('form.keyName')} name="name" placeholder="cursor-local" />
            <div className="form-row">
              <Field label={t('form.requestsPerMinute')} name="rpm" type="number" defaultValue="60" />
              <Field label={t('form.tokensPerMinute')} name="tpm" type="number" defaultValue="0" />
            </div>
            <div className="form-row">
              <Field label={t('form.concurrency')} name="max_concurrency" type="number" defaultValue="0" />
              <Field label={t('form.budgetUsd')} name="budget_usd" type="number" step="any" defaultValue="0" />
            </div>
            <label>{t('form.team')}<select name="team_id" defaultValue=""><option value="">{t('form.teamNone')}</option>{data.teams.map(team => <option key={team.id} value={team.id}>{team.name}</option>)}</select></label>
            <fieldset><legend>{t('form.allowedModels')}</legend>{data.models.map(model => <label className="check-row" key={model.id}><input type="checkbox" name="models" value={model.alias} /><span>{model.alias}</span><small>{model.upstream_model}</small></label>)}</fieldset>
            <FormActions pending={isPending} t={t} />
          </form>
        </Modal>
      )}
      {modal === 'team' && (
        <Modal title={t('modal.team.title')} onClose={() => setModal(null)} t={t}>
          <form onSubmit={submitTeam}>
            <Field label={t('form.teamName')} name="name" placeholder="engineering" />
            <Field label={t('form.budgetUsd')} name="budget_usd" type="number" step="any" defaultValue="0" />
            <FormActions pending={isPending} t={t} />
          </form>
        </Modal>
      )}
      {modal === 'batch' && (
        <Modal title={t('modal.batch.title')} onClose={() => setModal(null)} t={t}>
          {(() => {
            const provider = data.providers.find(item => item.id === batchProviderId);
            if (provider?.adapter === 'openai-codex') {
              return (
                <form onSubmit={submitBatchCodex}>
                  <label>{t('form.provider')}
                    <select name="provider_id" value={batchProviderId} onChange={event => setBatchProviderId(event.target.value)} required>
                      <option value="" disabled>{t('form.selectProvider')}</option>
                      {data.providers.filter(item => item.adapter === 'openai-codex').map(item => <option key={item.id} value={item.id}>{item.name}</option>)}
                    </select>
                  </label>
                  <p className="field-hint">{t('modal.batch.codexHint')}</p>
                  <label>{t('form.batchCodexFiles')}<input type="file" accept="application/json,.json" multiple onChange={event => setBatchCodexFiles(Array.from(event.target.files ?? []))} /></label>
                  <label>{t('form.batchCodexEntries')}<textarea name="entries" rows={8} placeholder={t('form.batchCodexEntriesPlaceholder')} /></label>
                  <label>{t('form.concurrency')}<input name="max_concurrency" type="number" min="1" defaultValue="1" /></label>
                  <FormActions pending={isPending} t={t} />
                </form>
              );
            }
            return (
              <form onSubmit={submitBatchAccounts}>
                <label>{t('form.provider')}
                  <select name="provider_id" value={batchProviderId} onChange={event => setBatchProviderId(event.target.value)} required>
                    <option value="" disabled>{t('form.selectProvider')}</option>
                    {data.providers.map(provider => <option key={provider.id} value={provider.id}>{provider.name}</option>)}
                  </select>
                </label>
                {(() => {
                  const target = data.providers.find(item => item.id === batchProviderId);
                  if (target?.adapter === 'anthropic') {
                    return (
                      <label>{t('form.credentialMethod')}
                        <select name="kind" defaultValue="api_key">
                          <option value="api_key">{t('credential.kind.apiKey')}</option>
                          <option value="cookie">{t('credential.kind.cookie')}</option>
                          <option value="oauth">{t('credential.kind.oauth')}</option>
                        </select>
                      </label>
                    );
                  }
                  return <input type="hidden" name="kind" value="api_key" />;
                })()}
                <label>{t('form.batchEntries')}<textarea name="entries" rows={10} placeholder={t('form.batchEntriesPlaceholder')} required /></label>
                <div className="form-row">
                  <Field label={t('form.priority')} name="priority" type="number" defaultValue="1" />
                  <Field label={t('form.concurrency')} name="max_concurrency" type="number" defaultValue="100" />
                </div>
                <FormActions pending={isPending} t={t} />
              </form>
            );
          })()}
        </Modal>
      )}
      {modal === 'batch-models' && (
        <Modal title={t('modal.batchModels.title')} onClose={() => setModal(null)} t={t}>
          <form onSubmit={submitBatchModels}>
            <label>{t('form.batchModelLines')}<textarea name="lines" rows={10} placeholder={t('form.batchModelLinesPlaceholder')} required /></label>
            <fieldset><legend>{t('form.eligibleAccounts')}</legend>{data.accounts.map(account => <label className="check-row" key={account.id}><input type="checkbox" name="accounts" value={account.id} defaultChecked /><span>{account.display_name}</span><small>{account.name}</small></label>)}</fieldset>
            <fieldset><legend>{t('form.pricing')}</legend><div className="form-row">
              <Field label={t('form.inputPerMtok')} name="input_per_mtok" type="number" step="any" defaultValue="0" />
              <Field label={t('form.outputPerMtok')} name="output_per_mtok" type="number" step="any" defaultValue="0" />
            </div></fieldset>
            <fieldset><legend>{t('form.fallbacks')}</legend>{data.models.map(model => <label className="check-row" key={model.id}><input type="checkbox" name="fallbacks" value={model.alias} /><span>{model.alias}</span><small>{model.upstream_model}</small></label>)}<small className="field-hint">{t('form.fallbackHint')}</small></fieldset>
            <FormActions pending={isPending} t={t} />
          </form>
        </Modal>
      )}
      {modal === 'routing' && (
        <Modal title={t('modal.routing.title')} onClose={() => setModal(null)} t={t}>
          <form onSubmit={submitRouting}>
            <label>{t('form.strategy')}
              <select name="strategy" defaultValue={data.routing?.strategy ?? 'balanced'}>
                {['balanced', 'priority'].map(option => <option key={option} value={option}>{option}</option>)}
              </select>
            </label>
            <div className="form-row">
              <Field label={t('form.stickyTtl')} name="sticky_ttl" defaultValue={data.routing?.sticky_ttl ?? '1h'} />
              <Field label={t('form.maxAttempts')} name="max_attempts" type="number" defaultValue={String(data.routing?.max_attempts ?? 2)} />
            </div>
            <div className="form-row three">
              <Field label={t('form.cooldown')} name="cooldown" defaultValue={data.routing?.resilience?.cooldown ?? '30s'} />
              <Field label={t('form.circuitFailures')} name="circuit_failures" type="number" defaultValue={String(data.routing?.resilience?.circuit_failures ?? 3)} />
              <Field label={t('form.circuitReset')} name="circuit_reset" defaultValue={data.routing?.resilience?.circuit_reset ?? '1m'} />
              <Field label={t('form.lockoutFailures')} name="lockout_failures" type="number" defaultValue={String(data.routing?.resilience?.lockout_failures ?? 10)} />
              <Field label={t('form.lockoutDuration')} name="lockout_duration" defaultValue={data.routing?.resilience?.lockout_duration ?? '15m'} />
            </div>
            <fieldset><legend>{t('settings.optimization.title')}</legend>
              <label className="check-row"><input type="checkbox" name="rtk_compression" defaultChecked={data.routing?.optimization?.rtk_compression ?? false} /><span>{t('form.rtkCompression')}</span><small>{t('form.rtkCompressionHint')}</small></label>
              <label className="check-row"><input type="checkbox" name="caveman" defaultChecked={data.routing?.optimization?.caveman ?? false} /><span>{t('form.caveman')}</span><small>{t('form.cavemanHint')}</small></label>
              <label className="check-row"><input type="checkbox" name="headroom" defaultChecked={data.routing?.optimization?.headroom ?? false} /><span>{t('form.headroom')}</span><small>{t('form.headroomHint')}</small></label>
              <label className="field"><span>{t('form.headroomReserve')}</span><input type="number" name="headroom_reserve" defaultValue={data.routing?.optimization?.headroom_reserve ?? 120000} /></label>
              <label className="field"><span>{t('form.headroomKeep')}</span><input type="number" name="headroom_keep" defaultValue={data.routing?.optimization?.headroom_keep ?? 8} /></label>
            </fieldset>
            <FormActions pending={isPending} t={t} />
          </form>
        </Modal>
      )}
      {rotating && (
        <Modal title={t('modal.rotation.title', { name: rotating.display_name })} onClose={() => setRotating(null)} t={t}>
          <form onSubmit={submitRotation}>
            <Field label={t('form.newApiKey')} name="api_key" type="password" placeholder="sk-…" />
            <FormActions pending={isPending} t={t} />
          </form>
        </Modal>
      )}
      {editing && (
        <Modal title={t(`modal.${editing.kind}.editTitle` as MessageKey)} onClose={() => setEditing(null)} t={t}>
          <form onSubmit={submitEditing}>
            {editing.kind === 'provider' && <>
              <Field label={t('form.displayName')} name="name" defaultValue={editing.item.name} />
              <Field label={t('form.baseUrl')} name="base_url" type="url" defaultValue={editing.item.base_url} />
            </>}
            {editing.kind === 'account' && <>
              <Field label={t('form.displayName')} name="display_name" defaultValue={editing.item.display_name} />
              <Field label={t('form.baseUrl')} name="base_url" type="url" defaultValue={editing.item.base_url} />
              <div className="form-row">
                <Field label={t('form.priority')} name="priority" type="number" defaultValue={String(editing.item.priority)} />
                <Field label={t('form.concurrency')} name="max_concurrency" type="number" defaultValue={String(editing.item.max_concurrency)} />
              </div>
              <label>{t('form.proxy')}<textarea name="proxy" rows={2} defaultValue={String(editing.item.metadata?.proxy ?? '')} placeholder="http://user:pass@host:8080" spellCheck={false} /><small className="field-hint">{t('form.proxyHint')}</small></label>
            </>}
            {editing.kind === 'model' && <>
              <div className="form-row"><Field label={t('form.publicAlias')} name="alias" defaultValue={editing.item.alias} /><Field label={t('form.upstreamModel')} name="upstream_model" defaultValue={editing.item.upstream_model} /></div>
              {!editing.item.provider_id && <fieldset><legend>{t('form.eligibleAccounts')}</legend>{data.accounts.map(account => <label className="check-row" key={account.id}><input type="checkbox" name="accounts" value={account.id} defaultChecked={editing.item.accounts.includes(account.id)} /><span>{account.display_name}</span><small>{account.name}</small></label>)}</fieldset>}
              <fieldset><legend>{t('form.pricing')}</legend><div className="form-row">
                <Field label={t('form.inputPerMtok')} name="input_per_mtok" type="number" step="any" defaultValue={String(editing.item.input_per_mtok ?? 0)} />
                <Field label={t('form.outputPerMtok')} name="output_per_mtok" type="number" step="any" defaultValue={String(editing.item.output_per_mtok ?? 0)} />
              </div></fieldset>
              <fieldset><legend>{t('form.fallbacks')}</legend>{data.models.filter(model => model.id !== editing.item.id).map(model => <label className="check-row" key={model.id}><input type="checkbox" name="fallbacks" value={model.alias} defaultChecked={(editing.item.fallbacks ?? []).includes(model.alias)} /><span>{model.alias}</span><small>{model.upstream_model}</small></label>)}<small className="field-hint">{t('form.fallbackHint')}</small></fieldset>
            </>}
            {editing.kind === 'team' && <>
              <Field label={t('form.teamName')} name="name" defaultValue={editing.item.name} />
              <Field label={t('form.budgetUsd')} name="budget_usd" type="number" step="any" defaultValue={String(editing.item.budget_usd ?? 0)} />
            </>}
            {editing.kind === 'key' && <>
              <Field label={t('form.keyName')} name="name" defaultValue={editing.item.name} />
              <label>{t('form.team')}<select name="team_id" defaultValue={editing.item.team_id ?? ''}><option value="">{t('form.teamNone')}</option>{data.teams.map(team => <option key={team.id} value={team.id}>{team.name}</option>)}</select></label>
              <div className="form-row">
                <Field label={t('form.requestsPerMinute')} name="rpm" type="number" defaultValue={String(editing.item.rpm)} />
                <Field label={t('form.tokensPerMinute')} name="tpm" type="number" defaultValue={String(editing.item.tpm ?? 0)} />
              </div>
              <div className="form-row">
                <Field label={t('form.concurrency')} name="max_concurrency" type="number" defaultValue={String(editing.item.max_concurrency ?? 0)} />
                <Field label={t('form.budgetUsd')} name="budget_usd" type="number" step="any" defaultValue={String(editing.item.budget_usd ?? 0)} />
              </div>
              <fieldset><legend>{t('form.allowedModels')}</legend>{data.models.map(model => <label className="check-row" key={model.id}><input type="checkbox" name="models" value={model.alias} defaultChecked={editing.item.models?.includes(model.alias)} /><span>{model.alias}</span><small>{model.upstream_model}</small></label>)}</fieldset>
            </>}
            <FormActions pending={isPending} t={t} />
          </form>
        </Modal>
      )}
      {deleting && (
        <Modal title={t(deleting.kind === 'provider' ? 'delete.providerTitle' : deleting.kind === 'model' ? 'delete.modelTitle' : 'delete.accountTitle')} onClose={() => setDeleting(null)} t={t}>
          <p>{t(deleting.kind === 'provider' ? 'delete.providerBody' : deleting.kind === 'model' ? 'delete.modelBody' : 'delete.accountBody')}</p>
          <footer className="form-actions">
            <button className="secondary" type="button" onClick={() => setDeleting(null)}>{t('delete.cancel')}</button>
            <button className="danger" type="button" onClick={deleteResource} disabled={isPending}><TrashIcon size={14} />{t('delete.confirm')}</button>
          </footer>
        </Modal>
      )}
      {paletteOpen && (
        <CommandPalette
          data={data}
          t={t}
          onNavigate={navigate}
          onClose={() => setPaletteOpen(false)}
        />
      )}
    </div>
  );
}

function ClaudeAccountMiniCard({ account, t, isPending, onEdit, onToggle, onDelete }: {
  account: any;
  t: Translator;
  isPending: boolean;
  onEdit: () => void;
  onToggle: () => void;
  onDelete: () => void;
}) {
  const kind = String(account.metadata?.auth_method ?? 'api_key') as 'api_key' | 'cookie' | 'oauth';
  return (
    <article className="claude-account-card">
      <header>
        <div className="account-title"><span className="provider-glyph claude-glyph"><CpuIcon size={16} /></span><div><b>{account.display_name}</b><small>{account.name}</small></div></div>
        <span className={`status ${account.enabled ? '' : 'warn'}`}><CheckCircleFillIcon size={12} />{account.enabled ? t('accounts.enabled') : t('accounts.disabled')}</span>
      </header>
      <div className="codex-account-facts">
        <div><span>{t('accounts.column.credential')}</span><b>{t(({ api_key: 'credential.kind.apiKey', cookie: 'credential.kind.cookie', oauth: 'credential.kind.oauth' } as const)[kind])}</b></div>
        <div><span>{t('form.priority')}</span><b>P{account.priority}</b></div>
        <div><span>{t('form.concurrency')}</span><b>{account.max_concurrency}</b></div>
        {String(account.metadata?.proxy ?? '').trim() && <div><span>{t('accounts.proxy')}</span><b title={t('accounts.proxyTooltip')}>✓</b></div>}
      </div>
      {account.last_error_message && <div className="account-error" title={String(account.last_error_message)}><AlertFillIcon size={12} />{String(account.last_error_message).slice(0, 140)}</div>}
      <footer>
        <button className="ghost" onClick={onEdit} disabled={isPending}><PencilIcon size={13} />{t('action.edit')}</button>
        <button className="ghost" onClick={onToggle} disabled={isPending}>{account.enabled ? t('action.disable') : t('action.enable')}</button>
        <button className="danger-quiet" onClick={onDelete} disabled={isPending}><TrashIcon size={13} />{t('action.delete')}</button>
      </footer>
    </article>
  );
}

function StandardAccountTable({ accounts, providerById, t, isPending, onDiscover, onRotate, onEdit, onToggle, onDelete, onAdd }: {
  accounts: any[];
  providerById: Map<string, any>;
  t: Translator;
  isPending: boolean;
  onDiscover: (account: any) => void;
  onRotate: (account: any) => void;
  onEdit: (account: any) => void;
  onToggle: (account: any) => void;
  onDelete: (account: any) => void;
  onAdd: () => void;
}) {
  return (
    <div className="table-card">
      <table>
        <thead><tr><th>{t('accounts.column.account')}</th><th>{t('accounts.column.provider')}</th><th>{t('accounts.column.endpoint')}</th><th>{t('accounts.column.routing')}</th><th>{t('accounts.column.credential')}</th><th>{t('accounts.column.status')}</th><th /></tr></thead>
        <tbody>
          {accounts.map(account => (
            <tr key={account.id}>
              <td><div className="cell-title"><span className="provider-glyph"><ServerIcon size={16} /></span><div><b>{account.display_name}</b><small>{account.name}</small></div></div></td>
              <td>{providerById.get(account.provider_id)?.name ?? t('accounts.unknown')}</td>
              <td><code className="truncate">{account.base_url}</code>
                {String(account.metadata?.proxy ?? '').trim() && <div className="account-proxy-badge" title={t('accounts.proxyTooltip')}>{t('accounts.proxy')}</div>}
              </td>
              <td>P{account.priority}</td>
              <td>{account.last_error_message
                ? <Status good={false}><span className="error-truncate" title={String(account.last_error_message)}>{String(account.last_error_message).slice(0, 60)}</span></Status>
                : <Status good={account.secret_present}>{account.secret_present ? t('accounts.encrypted', { version: account.key_version }) : t('accounts.missing')}</Status>}</td>
              <td><Status good={account.enabled}>{account.enabled ? t('accounts.enabled') : t('accounts.disabled')}</Status></td>
              <td><div className="row-actions">
                <button className="ghost" onClick={() => onDiscover(account)} disabled={isPending || !account.enabled}><SyncIcon size={14} />{t('action.discoverModels')}</button>
                <button className="ghost" onClick={() => onRotate(account)} disabled={isPending}><ShieldLockIcon size={14} />{t('action.rotate')}</button>
                <button className="ghost" onClick={() => onEdit(account)} disabled={isPending}><PencilIcon size={13} />{t('action.edit')}</button>
                <button className="ghost" onClick={() => onToggle(account)} disabled={isPending}>{account.enabled ? t('action.disable') : t('action.enable')}</button>
                <button className="danger-quiet" onClick={() => onDelete(account)} disabled={isPending}><TrashIcon size={13} />{t('action.delete')}</button>
              </div></td>
            </tr>
          ))}
        </tbody>
      </table>
      {!accounts.length && <Empty title={t('accounts.emptyTitle')} body={t('accounts.emptyBody')} onClick={onAdd} />}
    </div>
  );
}

function ResourcePage({ eyebrow, title, description, action, children }: {
  eyebrow: string;
  title: string;
  description: string;
  action?: React.ReactNode;
  children: React.ReactNode;
}) {  return (
    <section className="page">
      <div className="page-heading compact">
        <div><span className="eyebrow">{eyebrow}</span><h1>{title}</h1><p>{description}</p></div>
        <div className="heading-actions">{action}</div>
      </div>
      {children}
    </section>
  );
}

function Empty({ title, body, onClick }: { title: string; body: string; onClick: () => void }) {
  return <button className="empty-state" onClick={onClick}><PlusIcon size={24} /><b>{title}</b><span>{body}</span></button>;
}

function Field({ label, name, type = 'text', placeholder, defaultValue, step }: {
  label: string;
  name: string;
  type?: string;
  placeholder?: string;
  defaultValue?: string;
  step?: string;
}) {
  return <label>{label}<input name={name} type={type} placeholder={placeholder} defaultValue={defaultValue} step={step} required /></label>;
}

function FormActions({ pending, t }: { pending: boolean; t: Translator }) {
  return (
    <footer className="form-actions">
      <span>{t('form.draftHint')}</span>
      <button className="primary" disabled={pending}>{pending ? t('action.saving') : t('action.save')}</button>
    </footer>
  );
}
