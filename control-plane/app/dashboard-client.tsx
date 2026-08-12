'use client';

import {
  AlertFillIcon,
  CheckCircleFillIcon,
  CopyIcon,
  CpuIcon,
  DatabaseIcon,
  GearIcon,
  GitBranchIcon,
  HistoryIcon,
  HomeIcon,
  KeyIcon,
  PlusIcon,
  PencilIcon,
  PulseIcon,
  RocketIcon,
  ServerIcon,
  ShieldLockIcon,
  StackIcon,
  SyncIcon,
  TrashIcon,
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
import { CODEX_AUTH_CHANNEL } from './codex-client';
import { CodexAccountModal } from './components/codex-account-modal';
import { CodexAccountCard } from './components/codex-account-card';
import { ModelDiscoveryModal } from './components/model-discovery-modal';
import { ModelCard } from './components/model-card';
import { accountDefaultsForProvider } from './account-form';

type ResourceMap = {
  overview: Record<string, number | null>;
  providers: any[];
  accounts: any[];
  models: any[];
  keys: any[];
  versions: any[];
  audit: any[];
  routing: any;
};

type View = 'overview' | 'accounts' | 'models' | 'keys' | 'audit' | 'settings';
type UiError = { detail: string; fallback: MessageKey } | null;
type EditingResource = { kind: 'provider' | 'account' | 'model' | 'key'; item: any } | null;
type DeletingResource = { kind: 'provider' | 'account' | 'model'; item: any } | null;

const emptyData: ResourceMap = {
  overview: {},
  providers: [],
  accounts: [],
  models: [],
  keys: [],
  versions: [],
  audit: [],
  routing: null,
};

const nav = [
  ['overview', 'nav.overview', HomeIcon],
  ['accounts', 'nav.accounts', ServerIcon],
  ['models', 'nav.models', GitBranchIcon],
  ['keys', 'nav.keys', KeyIcon],
  ['audit', 'nav.audit', HistoryIcon],
  ['settings', 'nav.settings', GearIcon],
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

export default function DashboardClient({ initialLocale }: { initialLocale: Locale }) {
  const [locale, setLocale] = useState<Locale>(initialLocale);
  const [view, setView] = useState<View>('overview');
  const [data, setData] = useState<ResourceMap>(emptyData);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<UiError>(null);
  const [modal, setModal] = useState<'provider' | 'account' | 'codex' | 'discovery' | 'model' | 'key' | 'routing' | null>(null);
  const [discoveryAccountId, setDiscoveryAccountId] = useState<string | undefined>();
  const [rotating, setRotating] = useState<any>(null);
  const [editing, setEditing] = useState<EditingResource>(null);
  const [deleting, setDeleting] = useState<DeletingResource>(null);
  const [accountDraft, setAccountDraft] = useState({ providerId: '', baseURL: '' });
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
    const status = params.get('codex_status');
    if (!status) return;
    const accountId = params.get('account_id') ?? undefined;
    const channel = new BroadcastChannel(CODEX_AUTH_CHANNEL);
    channel.postMessage({ status, accountId });
    channel.close();
    params.delete('codex_status');
    params.delete('account_id');
    const query = params.toString();
    window.history.replaceState({}, '', `${window.location.pathname}${query ? `?${query}` : ''}${window.location.hash}`);
    if (window.name === '2papi-codex-auth') window.setTimeout(() => window.close(), 250);
  }, []);

  const selectLocale = useCallback((nextLocale: Locale) => {
    setLocale(nextLocale);
    persistLocale(nextLocale);
  }, []);

  const load = useCallback(async () => {
    setError(null);
    try {
      const [overview, providers, accounts, models, keys, versions, audit, routing] = await Promise.all([
        api('overview'),
        api('providers'),
        api('accounts'),
        api('models'),
        api('virtual-keys'),
        api('config-versions'),
        api('audit-events'),
        api('routing'),
      ]);
      setData({ overview, providers, accounts, models, keys, versions, audit, routing });
    } catch (cause) {
      setError(uiError(cause, 'error.controlUnavailable'));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  useEffect(() => {
    const channel = new BroadcastChannel(CODEX_AUTH_CHANNEL);
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
  const codexAccounts = useMemo(
    () => data.accounts.filter(account => codexProviderIds.has(account.provider_id)),
    [codexProviderIds, data.accounts],
  );
  const standardAccounts = useMemo(
    () => data.accounts.filter(account => !codexProviderIds.has(account.provider_id)),
    [codexProviderIds, data.accounts],
  );
  const currentNavLabel = t(nav.find(item => item[0] === view)?.[1] ?? 'nav.overview');

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
        adapter: 'openai-compatible',
        base_url: form.get('base_url'),
        enabled: true,
        metadata: {},
      }),
    }));
  }

  function submitAccount(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
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
        credential: { api_key: form.get('api_key') },
        metadata: {},
      }),
    }));
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

  function toggleResource(resource: 'providers' | 'models' | 'virtual-keys', item: any) {
    mutate(() => api(`${resource}/${item.id}`, {
      method: 'PATCH',
      body: JSON.stringify({ enabled: !item.enabled }),
    }));
  }

  function openAccountModal() {
    setAccountDraft({ providerId: '', baseURL: '' });
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

  function updateModelStrategy(model: any, routing_strategy: 'round_robin' | 'quota_failover') {
    mutate(() => api(`models/${model.id}`, { method: 'PATCH', body: JSON.stringify({ routing_strategy }) }));
  }

  function submitEditing(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!editing) return;
    const form = new FormData(event.currentTarget);
    const routes = { provider: 'providers', account: 'accounts', model: 'models', key: 'virtual-keys' } as const;
    let body: Record<string, unknown>;
    switch (editing.kind) {
      case 'provider':
        body = { name: form.get('name'), base_url: form.get('base_url') };
        break;
      case 'account':
        body = {
          display_name: form.get('display_name'), base_url: form.get('base_url'),
          priority: Number(form.get('priority')),
          max_concurrency: Number(form.get('max_concurrency')),
        };
        break;
      case 'model':
        body = { alias: form.get('alias'), upstream_model: form.get('upstream_model'), ...(!editing.item.provider_id ? { accounts: form.getAll('accounts') } : {}) };
        break;
      case 'key':
        body = { name: form.get('name'), rpm: Number(form.get('rpm')), models: form.getAll('models') };
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
            <button key={id} className={view === id ? 'active' : ''} onClick={() => setView(id)}>
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
          <div><span className="breadcrumb">2papi / {currentNavLabel}</span></div>
          <div className="top-actions">
            <LocaleSwitch locale={locale} onChange={selectLocale} t={t} mobile />
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
            <div className="overview-grid">
              <article className="panel account-panel">
                <div className="panel-heading">
                  <div><span className="eyebrow">{t('overview.pool.eyebrow')}</span><h2>{t('overview.pool.title')}</h2></div>
                  <button className="ghost" onClick={() => setView('accounts')}>{t('action.manageAll')}</button>
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

        {view === 'accounts' && (
          <ResourcePage
            eyebrow={t('accounts.eyebrow')}
            title={t('accounts.title')}
            description={t('accounts.description')}
            action={<>
              <button className="primary codex-action" onClick={() => setModal('codex')}><PlusIcon size={16} />{t('action.addCodexAccount')}</button>
              <button className="secondary" onClick={() => setModal('provider')}><PlusIcon size={16} />{t('action.addProvider')}</button>
              <button className="secondary" onClick={openAccountModal}><PlusIcon size={16} />{t('action.addAccount')}</button>
            </>}
          >
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
                  <td><div className="row-actions"><button className="ghost" onClick={() => setEditing({ kind: 'provider', item: provider })}><PencilIcon size={13} />{t('action.edit')}</button><button className="ghost" onClick={() => toggleResource('providers', provider)}>{provider.enabled ? t('action.disable') : t('action.enable')}</button><button className="danger-quiet" onClick={() => setDeleting({ kind: 'provider', item: provider })}><TrashIcon size={13} />{t('action.delete')}</button></div></td>
                </tr>)}</tbody>
              </table>
            </div>
            <div className="section-heading">
              <div><h2>{t('accounts.codexTitle')}</h2><p>{t('accounts.codexDescription')}</p></div>
              <button className="ghost" onClick={() => { setDiscoveryAccountId(undefined); setModal('discovery'); }} disabled={!codexAccounts.length}><SyncIcon size={14} />{t('action.discoverModels')}</button>
            </div>
            <div className="codex-account-grid">
              {codexAccounts.map(account => (
                <CodexAccountCard
                  key={account.id}
                  account={account}
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
            <div className="table-card">
              <table>
                <thead><tr><th>{t('accounts.column.account')}</th><th>{t('accounts.column.provider')}</th><th>{t('accounts.column.endpoint')}</th><th>{t('accounts.column.routing')}</th><th>{t('accounts.column.credential')}</th><th>{t('accounts.column.status')}</th><th /></tr></thead>
                <tbody>
                  {standardAccounts.map(account => (
                    <tr key={account.id}>
                      <td><div className="cell-title"><span className="provider-glyph"><ServerIcon size={16} /></span><div><b>{account.display_name}</b><small>{account.name}</small></div></div></td>
                      <td>{providerById.get(account.provider_id)?.name ?? t('accounts.unknown')}</td>
                      <td><code className="truncate">{account.base_url}</code></td>
                      <td>P{account.priority}</td>
                      <td><Status good={account.secret_present}>{account.secret_present ? t('accounts.encrypted', { version: account.key_version }) : t('accounts.missing')}</Status></td>
                      <td><Status good={account.enabled}>{account.enabled ? t('accounts.enabled') : t('accounts.disabled')}</Status></td>
                      <td><div className="row-actions">
                        <button className="ghost" onClick={() => { setDiscoveryAccountId(account.id); setModal('discovery'); }} disabled={isPending || !account.enabled}><SyncIcon size={14} />{t('action.discoverModels')}</button>
                        <button className="ghost" onClick={() => setRotating(account)} disabled={isPending}><ShieldLockIcon size={14} />{t('action.rotate')}</button>
                        <button className="ghost" onClick={() => setEditing({ kind: 'account', item: account })} disabled={isPending}><PencilIcon size={13} />{t('action.edit')}</button>
                        <button className="ghost" onClick={() => toggleAccount(account)} disabled={isPending}>{account.enabled ? t('action.disable') : t('action.enable')}</button>
                        <button className="danger-quiet" onClick={() => setDeleting({ kind: 'account', item: account })} disabled={isPending}><TrashIcon size={13} />{t('action.delete')}</button>
                      </div></td>
                    </tr>
                  ))}
                </tbody>
              </table>
              {!standardAccounts.length && <Empty title={t('accounts.emptyTitle')} body={t('accounts.emptyBody')} onClick={openAccountModal} />}
            </div>
          </ResourcePage>
        )}

        {view === 'models' && (
          <ResourcePage
            eyebrow={t('models.eyebrow')}
            title={t('models.title')}
            description={t('models.description')}
            action={<>
              <button className="secondary" onClick={() => { setDiscoveryAccountId(undefined); setModal('discovery'); }}><SyncIcon size={16} />{t('action.discoverModels')}</button>
              <button className="primary" onClick={() => setModal('model')}><PlusIcon size={16} />{t('action.newModelRoute')}</button>
            </>}
          >
            <div className="model-grid">
              {data.models.map(model => <ModelCard key={model.id} model={model} locale={locale} t={t} onEdit={() => setEditing({ kind: 'model', item: model })} onToggle={() => toggleResource('models', model)} onDelete={() => setDeleting({ kind: 'model', item: model })} onStrategy={strategy => updateModelStrategy(model, strategy)} />)}
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
                <thead><tr><th>{t('keys.column.name')}</th><th>{t('keys.column.prefix')}</th><th>{t('keys.column.models')}</th><th>{t('keys.column.rpm')}</th><th>{t('keys.column.created')}</th><th>{t('keys.column.status')}</th><th /></tr></thead>
                <tbody>
                  {data.keys.map(key => (
                    <tr key={key.id}>
                      <td><div className="cell-title"><span className="provider-glyph tone-3"><KeyIcon size={16} /></span><b>{key.name}</b></div></td>
                      <td><code>{key.key_prefix}…</code></td>
                      <td>{key.models?.length ? key.models.join(', ') : t('keys.allModels')}</td>
                      <td>{key.rpm}</td>
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
            </div>
          </ResourcePage>
        )}

        {loading && <div className="loading-layer"><span /><b>{t('loading.controlPlane')}</b></div>}
      </main>

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
            <Field label={t('form.displayName')} name="name" placeholder="OpenAI" />
            <Field label={t('form.slug')} name="slug" placeholder="openai" />
            <Field label={t('form.baseUrl')} name="base_url" placeholder="https://api.openai.com" type="url" />
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
              <Field label={t('form.internalName')} name="name" placeholder="openai-primary" />
              <Field label={t('form.displayName')} name="display_name" placeholder="OpenAI Primary" />
            </div>
            <label>{t('form.baseUrl')}<input name="base_url" type="url" value={accountDraft.baseURL} readOnly required /></label>
            <Field label={t('form.apiKey')} name="api_key" placeholder="sk-…" type="password" />
            <div className="form-row">
              <Field label={t('form.priority')} name="priority" type="number" defaultValue="1" />
              <Field label={t('form.concurrency')} name="max_concurrency" type="number" defaultValue="100" />
            </div>
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
            <FormActions pending={isPending} t={t} />
          </form>
        </Modal>
      )}
      {modal === 'key' && (
        <Modal title={t('modal.key.title')} onClose={() => setModal(null)} t={t}>
          <form onSubmit={submitKey}>
            <Field label={t('form.keyName')} name="name" placeholder="cursor-local" />
            <Field label={t('form.requestsPerMinute')} name="rpm" type="number" defaultValue="60" />
            <fieldset><legend>{t('form.allowedModels')}</legend>{data.models.map(model => <label className="check-row" key={model.id}><input type="checkbox" name="models" value={model.alias} /><span>{model.alias}</span><small>{model.upstream_model}</small></label>)}</fieldset>
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
            </div>
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
            </>}
            {editing.kind === 'model' && <>
              <div className="form-row"><Field label={t('form.publicAlias')} name="alias" defaultValue={editing.item.alias} /><Field label={t('form.upstreamModel')} name="upstream_model" defaultValue={editing.item.upstream_model} /></div>
              {!editing.item.provider_id && <fieldset><legend>{t('form.eligibleAccounts')}</legend>{data.accounts.map(account => <label className="check-row" key={account.id}><input type="checkbox" name="accounts" value={account.id} defaultChecked={editing.item.accounts.includes(account.id)} /><span>{account.display_name}</span><small>{account.name}</small></label>)}</fieldset>}
            </>}
            {editing.kind === 'key' && <>
              <Field label={t('form.keyName')} name="name" defaultValue={editing.item.name} />
              <Field label={t('form.requestsPerMinute')} name="rpm" type="number" defaultValue={String(editing.item.rpm)} />
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
    </div>
  );
}

function ResourcePage({ eyebrow, title, description, action, children }: {
  eyebrow: string;
  title: string;
  description: string;
  action?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
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
