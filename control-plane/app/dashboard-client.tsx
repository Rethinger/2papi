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
  PulseIcon,
  RocketIcon,
  ServerIcon,
  ShieldLockIcon,
  StackIcon,
  SyncIcon,
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
  const [modal, setModal] = useState<'provider' | 'account' | 'model' | 'key' | 'routing' | null>(null);
  const [rotating, setRotating] = useState<any>(null);
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

  const latestVersion = data.versions[0];
  const enabledAccounts = data.accounts.filter(account => account.enabled);
  const providerById = useMemo(
    () => new Map(data.providers.map(provider => [provider.id, provider])),
    [data.providers],
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
        base_url: form.get('base_url'),
        enabled: true,
        priority: Number(form.get('priority') || 1),
        weight: Number(form.get('weight') || 1),
        max_concurrency: Number(form.get('max_concurrency') || 100),
        cost: Number(form.get('cost') || 0),
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
              <button className="primary" onClick={() => setModal('account')}>
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
                          <span>{t('overview.pool.weight')} <b>{account.weight}</b></span>
                          <span>{t('overview.pool.limit')} <b>{account.max_concurrency}</b></span>
                        </div>
                        <div className="quota"><span style={{ width: `${capacity}%` }} /></div>
                        <small className="quota-copy">{t('overview.pool.capacity', { percent: capacity })}</small>
                      </div>
                    );
                  })}
                  {!data.accounts.length && (
                    <button className="empty-card" onClick={() => setModal('account')}>
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
              <button className="secondary" onClick={() => setModal('provider')}><PlusIcon size={16} />{t('action.addProvider')}</button>
              <button className="primary" onClick={() => setModal('account')}><PlusIcon size={16} />{t('action.addAccount')}</button>
            </>}
          >
            <div className="table-card">
              <table>
                <thead><tr><th>{t('accounts.column.account')}</th><th>{t('accounts.column.provider')}</th><th>{t('accounts.column.endpoint')}</th><th>{t('accounts.column.routing')}</th><th>{t('accounts.column.credential')}</th><th>{t('accounts.column.status')}</th><th /></tr></thead>
                <tbody>
                  {data.accounts.map(account => (
                    <tr key={account.id}>
                      <td><div className="cell-title"><span className="provider-glyph"><ServerIcon size={16} /></span><div><b>{account.display_name}</b><small>{account.name}</small></div></div></td>
                      <td>{providerById.get(account.provider_id)?.name ?? t('accounts.unknown')}</td>
                      <td><code className="truncate">{account.base_url}</code></td>
                      <td>P{account.priority} · W{account.weight}</td>
                      <td><Status good={account.secret_present}>{account.secret_present ? t('accounts.encrypted', { version: account.key_version }) : t('accounts.missing')}</Status></td>
                      <td><Status good={account.enabled}>{account.enabled ? t('accounts.enabled') : t('accounts.disabled')}</Status></td>
                      <td><div className="row-actions">
                        <button className="ghost" onClick={() => setRotating(account)} disabled={isPending}><ShieldLockIcon size={14} />{t('action.rotate')}</button>
                        <button className="ghost" onClick={() => toggleAccount(account)} disabled={isPending}>{account.enabled ? t('action.disable') : t('action.enable')}</button>
                      </div></td>
                    </tr>
                  ))}
                </tbody>
              </table>
              {!data.accounts.length && <Empty title={t('accounts.emptyTitle')} body={t('accounts.emptyBody')} onClick={() => setModal('account')} />}
            </div>
          </ResourcePage>
        )}

        {view === 'models' && (
          <ResourcePage
            eyebrow={t('models.eyebrow')}
            title={t('models.title')}
            description={t('models.description')}
            action={<button className="primary" onClick={() => setModal('model')}><PlusIcon size={16} />{t('action.newModelRoute')}</button>}
          >
            <div className="model-grid">
              {data.models.map(model => (
                <article className="model-card" key={model.id}>
                  <header><span className="provider-glyph tone-2"><GitBranchIcon size={16} /></span><Status good={model.enabled}>{model.enabled ? t('models.available') : t('models.disabled')}</Status></header>
                  <h3>{model.alias}</h3>
                  <code>{model.upstream_model}</code>
                  <div className="route-chips">{model.accounts.map((id: string) => <span key={id}>{data.accounts.find(account => account.id === id)?.name ?? id.slice(0, 8)}</span>)}</div>
                  <footer><span>{modelAccountCount(model.accounts.length, locale, t)}</span><span>{data.routing?.strategy ?? 'balanced'}</span></footer>
                </article>
              ))}
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
                <thead><tr><th>{t('keys.column.name')}</th><th>{t('keys.column.prefix')}</th><th>{t('keys.column.models')}</th><th>{t('keys.column.rpm')}</th><th>{t('keys.column.created')}</th><th>{t('keys.column.status')}</th></tr></thead>
                <tbody>
                  {data.keys.map(key => (
                    <tr key={key.id}>
                      <td><div className="cell-title"><span className="provider-glyph tone-3"><KeyIcon size={16} /></span><b>{key.name}</b></div></td>
                      <td><code>{key.key_prefix}…</code></td>
                      <td>{key.models?.length ? key.models.join(', ') : t('keys.allModels')}</td>
                      <td>{key.rpm}</td>
                      <td>{new Date(key.created_at).toLocaleDateString(dateLocale(locale))}</td>
                      <td><Status good={key.enabled}>{key.enabled ? t('keys.active') : t('keys.disabled')}</Status></td>
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
              <select name="provider_id" required defaultValue="">
                <option value="" disabled>{t('form.selectProvider')}</option>
                {data.providers.map(provider => <option key={provider.id} value={provider.id}>{provider.name}</option>)}
              </select>
            </label>
            <div className="form-row">
              <Field label={t('form.internalName')} name="name" placeholder="openai-primary" />
              <Field label={t('form.displayName')} name="display_name" placeholder="OpenAI Primary" />
            </div>
            <Field label={t('form.baseUrl')} name="base_url" placeholder="https://api.openai.com" type="url" />
            <Field label={t('form.apiKey')} name="api_key" placeholder="sk-…" type="password" />
            <div className="form-row three">
              <Field label={t('form.priority')} name="priority" type="number" defaultValue="1" />
              <Field label={t('form.weight')} name="weight" type="number" defaultValue="1" />
              <Field label={t('form.concurrency')} name="max_concurrency" type="number" defaultValue="100" />
            </div>
            <Field label={t('form.costWeight')} name="cost" type="number" defaultValue="0" step="0.001" />
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
                {['balanced', 'priority', 'weighted'].map(option => <option key={option} value={option}>{option}</option>)}
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
