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
  KebabHorizontalIcon,
  PlusIcon,
  PulseIcon,
  RocketIcon,
  ServerIcon,
  ShieldLockIcon,
  StackIcon,
  SyncIcon,
} from '@primer/octicons-react';
import { FormEvent, useCallback, useEffect, useMemo, useState, useTransition } from 'react';

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

const emptyData: ResourceMap = { overview: {}, providers: [], accounts: [], models: [], keys: [], versions: [], audit: [], routing: null };

const nav = [
  ['overview', 'Overview', HomeIcon],
  ['accounts', 'Accounts', ServerIcon],
  ['models', 'Models & routes', GitBranchIcon],
  ['keys', 'API keys', KeyIcon],
  ['audit', 'Audit log', HistoryIcon],
  ['settings', 'Settings', GearIcon],
] as const;

async function api(path: string, init?: RequestInit) {
  const response = await fetch(`/api/control/v1/${path}`, { ...init, headers: { 'Content-Type': 'application/json', ...(init?.headers ?? {}) } });
  const payload = await response.json();
  if (!response.ok) throw new Error(payload?.error?.message ?? `Request failed (${response.status})`);
  return payload.data;
}

function Status({ good = true, children }: { good?: boolean; children: React.ReactNode }) {
  return <span className={`status ${good ? 'good' : 'warn'}`}>{good ? <CheckCircleFillIcon size={12} /> : <AlertFillIcon size={12} />}{children}</span>;
}

function Metric({ label, value, detail }: { label: string; value: string | number; detail: string }) {
  return <article className="metric-card"><span className="metric-label">{label}</span><strong>{value}</strong><span className="metric-detail">{detail}</span></article>;
}

function Modal({ title, children, onClose }: { title: string; children: React.ReactNode; onClose: () => void }) {
  return <div className="modal-backdrop" role="presentation" onMouseDown={onClose}><section className="modal" role="dialog" aria-modal="true" aria-label={title} onMouseDown={event => event.stopPropagation()}><header><div><span className="eyebrow">Create resource</span><h2>{title}</h2></div><button className="icon-button" onClick={onClose} aria-label="Close dialog">×</button></header>{children}</section></div>;
}

export default function DashboardClient() {
  const [view, setView] = useState<View>('overview');
  const [data, setData] = useState<ResourceMap>(emptyData);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [modal, setModal] = useState<'provider' | 'account' | 'model' | 'key' | null>(null);
  const [createdKey, setCreatedKey] = useState('');
  const [isPending, startTransition] = useTransition();

  const load = useCallback(async () => {
    setError('');
    try {
      const [overview, providers, accounts, models, keys, versions, audit, routing] = await Promise.all([
        api('overview'), api('providers'), api('accounts'), api('models'), api('virtual-keys'), api('config-versions'), api('audit-events'), api('routing'),
      ]);
      setData({ overview, providers, accounts, models, keys, versions, audit, routing });
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Control plane is unavailable');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  const latestVersion = data.versions[0];
  const enabledAccounts = data.accounts.filter(account => account.enabled);
  const providerById = useMemo(() => new Map(data.providers.map(provider => [provider.id, provider])), [data.providers]);

  function mutate(work: () => Promise<unknown>) {
    startTransition(async () => {
      setError('');
      try { await work(); setModal(null); await load(); }
      catch (cause) { setError(cause instanceof Error ? cause.message : 'Action failed'); }
    });
  }

  function submitProvider(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); const form = new FormData(event.currentTarget);
    mutate(() => api('providers', { method: 'POST', body: JSON.stringify({ slug: form.get('slug'), name: form.get('name'), adapter: 'openai-compatible', base_url: form.get('base_url'), enabled: true, metadata: {} }) }));
  }

  function submitAccount(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); const form = new FormData(event.currentTarget);
    mutate(() => api('accounts', { method: 'POST', body: JSON.stringify({ provider_id: form.get('provider_id'), name: form.get('name'), display_name: form.get('display_name'), base_url: form.get('base_url'), enabled: true, priority: Number(form.get('priority') || 1), weight: Number(form.get('weight') || 1), max_concurrency: Number(form.get('max_concurrency') || 100), cost: Number(form.get('cost') || 0), credential: { api_key: form.get('api_key') }, metadata: {} }) }));
  }

  function submitModel(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); const form = new FormData(event.currentTarget);
    mutate(() => api('models', { method: 'POST', body: JSON.stringify({ alias: form.get('alias'), upstream_model: form.get('upstream_model'), enabled: true, accounts: form.getAll('accounts') }) }));
  }

  function submitKey(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); const form = new FormData(event.currentTarget);
    startTransition(async () => {
      setError('');
      try {
        const result = await api('virtual-keys', { method: 'POST', body: JSON.stringify({ name: form.get('name'), models: form.getAll('models'), rpm: Number(form.get('rpm') || 60), enabled: true }) });
        setCreatedKey(result.plaintext_key); setModal(null); await load();
      } catch (cause) { setError(cause instanceof Error ? cause.message : 'Key creation failed'); }
    });
  }

  function publish() { mutate(() => api('config-versions/publish', { method: 'POST', body: '{}' })); }
  function toggleAccount(account: any) { mutate(() => api(`accounts/${account.id}`, { method: 'PATCH', body: JSON.stringify({ enabled: !account.enabled }) })); }

  return <div className="app-shell">
    <aside className="sidebar">
      <div className="brand"><span className="brand-mark"><PulseIcon size={16} /></span><div><b>2papi</b><small>CONTROL PLANE</small></div></div>
      <nav aria-label="Main navigation">{nav.map(([id, label, Icon]) => <button key={id} className={view === id ? 'active' : ''} onClick={() => setView(id)}><Icon size={16} /><span>{label}</span>{id === 'accounts' && <em>{data.accounts.length}</em>}</button>)}</nav>
      <div className="sidebar-foot"><div className="system-line"><span className="live-dot" /><div><b>Gateway online</b><small>localhost:18080</small></div></div><div className="build-label">Phase A · local admin</div></div>
    </aside>

    <main className="workspace">
      <header className="topbar"><div><span className="breadcrumb">2papi / {nav.find(item => item[0] === view)?.[1]}</span></div><div className="top-actions"><button className="secondary" onClick={() => void load()} disabled={loading}><SyncIcon size={16} />Refresh</button><button className="publish" onClick={publish} disabled={isPending}><RocketIcon size={16} />Publish changes</button></div></header>

      {error && <div className="alert"><AlertFillIcon size={16} /><div><b>Action required</b><span>{error}</span></div><button onClick={() => setError('')} aria-label="Dismiss">×</button></div>}
      {createdKey && <div className="secret-banner"><ShieldLockIcon size={16} /><div><b>Copy this key now. It will not be shown again.</b><code>{createdKey}</code></div><button onClick={() => navigator.clipboard.writeText(createdKey)}><CopyIcon size={16} />Copy</button><button className="icon-button" onClick={() => setCreatedKey('')} aria-label="Dismiss">×</button></div>}

      {view === 'overview' && <section className="page">
        <div className="page-heading"><div><span className="eyebrow">Routing workspace</span><h1>Your AI accounts, in one control room.</h1><p>Inspect the pool, publish routing changes, and keep every model available.</p></div><button className="primary" onClick={() => setModal('account')}><PlusIcon size={16} />Connect account</button></div>
        <div className="metrics"><Metric label="Active accounts" value={enabledAccounts.length} detail={`${data.accounts.length} configured`} /><Metric label="Public models" value={data.models.length} detail={`${data.overview.providers ?? 0} providers`} /><Metric label="Virtual keys" value={data.keys.length} detail="policy protected" /><Metric label="Active snapshot" value={latestVersion?.version ? `v${latestVersion.version}` : 'YAML'} detail={latestVersion?.status ?? 'fallback runtime'} /></div>
        <div className="overview-grid">
          <article className="panel account-panel"><div className="panel-heading"><div><span className="eyebrow">Account pool</span><h2>Connected capacity</h2></div><button className="ghost" onClick={() => setView('accounts')}>Manage all</button></div>
            <div className="account-grid">{data.accounts.slice(0, 4).map((account, index) => <div className="account-card" key={account.id}><div className="account-title"><span className={`provider-glyph tone-${index % 4}`}><CpuIcon size={16} /></span><div><b>{account.display_name}</b><small>{providerById.get(account.provider_id)?.name ?? account.name}</small></div><Status good={account.enabled}>{account.enabled ? 'Ready' : 'Disabled'}</Status></div><div className="account-meta"><span>Priority <b>{account.priority}</b></span><span>Weight <b>{account.weight}</b></span><span>Limit <b>{account.max_concurrency}</b></span></div><div className="quota"><span style={{ width: `${72 - index * 9}%` }} /></div><small className="quota-copy">Capacity estimate · {72 - index * 9}%</small></div>)}{!data.accounts.length && <button className="empty-card" onClick={() => setModal('account')}><PlusIcon size={24} /><b>Connect your first provider</b><span>Add an API key or compatible endpoint.</span></button>}</div>
          </article>
          <article className="panel route-panel"><div className="panel-heading"><div><span className="eyebrow">Live configuration</span><h2>Snapshot rail</h2></div><Status good={!error}>{error ? 'Degraded' : 'In sync'}</Status></div><div className="route-rail"><div className="rail-node"><DatabaseIcon size={16} /><div><b>PostgreSQL</b><span>desired state</span></div></div><i /><div className="rail-node"><StackIcon size={16} /><div><b>Snapshot {latestVersion?.version ? `v${latestVersion.version}` : 'fallback'}</b><span>{latestVersion?.status ?? 'local YAML'}</span></div></div><i /><div className="rail-node active"><ServerIcon size={16} /><div><b>Gateway</b><span>serving traffic</span></div></div></div><div className="version-list">{data.versions.slice(0, 3).map(version => <div key={version.version}><span>v{version.version}</span><code>{String(version.checksum).slice(0, 10)}</code><Status good={version.status === 'published'}>{version.status}</Status></div>)}{!data.versions.length && <p>No database snapshots yet. The gateway is safely using YAML.</p>}</div></article>
        </div>
      </section>}

      {view === 'accounts' && <ResourcePage eyebrow="Provider capacity" title="Accounts" description="Credentials, endpoints, priorities, and routing weight." action={<><button className="secondary" onClick={() => setModal('provider')}><PlusIcon size={16} />Provider</button><button className="primary" onClick={() => setModal('account')}><PlusIcon size={16} />Account</button></>}>
        <div className="table-card"><table><thead><tr><th>Account</th><th>Provider</th><th>Endpoint</th><th>Routing</th><th>Credential</th><th>Status</th><th /></tr></thead><tbody>{data.accounts.map(account => <tr key={account.id}><td><div className="cell-title"><span className="provider-glyph"><ServerIcon size={16} /></span><div><b>{account.display_name}</b><small>{account.name}</small></div></div></td><td>{providerById.get(account.provider_id)?.name ?? 'Unknown'}</td><td><code className="truncate">{account.base_url}</code></td><td>P{account.priority} · W{account.weight}</td><td><Status good={account.secret_present}>{account.secret_present ? `Encrypted v${account.key_version}` : 'Missing'}</Status></td><td><Status good={account.enabled}>{account.enabled ? 'Enabled' : 'Disabled'}</Status></td><td><button className="icon-button" onClick={() => toggleAccount(account)} aria-label={account.enabled ? 'Disable account' : 'Enable account'}><KebabHorizontalIcon size={16} /></button></td></tr>)}</tbody></table>{!data.accounts.length && <Empty title="No accounts connected" body="Connect an upstream account to make a model available." onClick={() => setModal('account')} />}</div>
      </ResourcePage>}

      {view === 'models' && <ResourcePage eyebrow="Public catalog" title="Models & routes" description="Map stable aliases to upstream deployments and account pools." action={<button className="primary" onClick={() => setModal('model')}><PlusIcon size={16} />New model route</button>}>
        <div className="model-grid">{data.models.map(model => <article className="model-card" key={model.id}><header><span className="provider-glyph tone-2"><GitBranchIcon size={16} /></span><Status good={model.enabled}>{model.enabled ? 'Available' : 'Disabled'}</Status></header><h3>{model.alias}</h3><code>{model.upstream_model}</code><div className="route-chips">{model.accounts.map((id: string) => <span key={id}>{data.accounts.find(account => account.id === id)?.name ?? id.slice(0, 8)}</span>)}</div><footer><span>{model.accounts.length} account{model.accounts.length === 1 ? '' : 's'}</span><span>{data.routing?.strategy ?? 'balanced'}</span></footer></article>)}{!data.models.length && <Empty title="No public models" body="Create an alias and attach at least one account." onClick={() => setModal('model')} />}</div>
      </ResourcePage>}

      {view === 'keys' && <ResourcePage eyebrow="Client access" title="API keys" description="Issue virtual keys without exposing upstream credentials." action={<button className="primary" onClick={() => setModal('key')}><PlusIcon size={16} />Create API key</button>}>
        <div className="table-card"><table><thead><tr><th>Name</th><th>Prefix</th><th>Models</th><th>RPM</th><th>Created</th><th>Status</th></tr></thead><tbody>{data.keys.map(key => <tr key={key.id}><td><div className="cell-title"><span className="provider-glyph tone-3"><KeyIcon size={16} /></span><b>{key.name}</b></div></td><td><code>{key.key_prefix}…</code></td><td>{key.models?.length ? key.models.join(', ') : 'All models'}</td><td>{key.rpm}</td><td>{new Date(key.created_at).toLocaleDateString()}</td><td><Status good={key.enabled}>{key.enabled ? 'Active' : 'Disabled'}</Status></td></tr>)}</tbody></table>{!data.keys.length && <Empty title="No virtual keys" body="Create one key for every app or environment." onClick={() => setModal('key')} />}</div>
      </ResourcePage>}

      {view === 'audit' && <ResourcePage eyebrow="Immutable history" title="Audit log" description="Every administrative change and snapshot publication." action={<button className="secondary" onClick={() => void load()}><SyncIcon size={16} />Refresh</button>}>
        <div className="timeline">{data.audit.map(event => <article key={event.id}><span className="timeline-dot" /><div><div className="timeline-title"><b>{event.action}</b><span>{event.resource_type}</span><time>{new Date(event.created_at).toLocaleString()}</time></div><code>{event.resource_id || 'system'}</code></div></article>)}{!data.audit.length && <div className="empty-inline">No administrative events yet.</div>}</div>
      </ResourcePage>}

      {view === 'settings' && <ResourcePage eyebrow="Local deployment" title="Settings" description="Runtime defaults and service connectivity for this installation.">
        <div className="settings-grid"><article className="panel"><ShieldLockIcon size={24} /><h3>Secret storage</h3><p>Envelope encryption is active. Credential plaintext is never returned by the API.</p><Status>Master key loaded</Status></article><article className="panel"><DatabaseIcon size={24} /><h3>Persistence</h3><p>PostgreSQL holds desired state. Redis announces configuration versions.</p><Status>Private services</Status></article><article className="panel"><ServerIcon size={24} /><h3>Gateway fallback</h3><p>The last valid runtime keeps serving if the control plane becomes unavailable.</p><Status>YAML fallback ready</Status></article></div>
      </ResourcePage>}

      {loading && <div className="loading-layer"><span /><b>Loading control plane</b></div>}
    </main>

    {modal === 'provider' && <Modal title="Add provider" onClose={() => setModal(null)}><form onSubmit={submitProvider}><Field label="Display name" name="name" placeholder="OpenAI" /><Field label="Slug" name="slug" placeholder="openai" /><Field label="Base URL" name="base_url" placeholder="https://api.openai.com" type="url" /><FormActions pending={isPending} /></form></Modal>}
    {modal === 'account' && <Modal title="Connect account" onClose={() => setModal(null)}><form onSubmit={submitAccount}><label>Provider<select name="provider_id" required defaultValue=""><option value="" disabled>Select provider</option>{data.providers.map(provider => <option key={provider.id} value={provider.id}>{provider.name}</option>)}</select></label><div className="form-row"><Field label="Internal name" name="name" placeholder="openai-primary" /><Field label="Display name" name="display_name" placeholder="OpenAI Primary" /></div><Field label="Base URL" name="base_url" placeholder="https://api.openai.com" type="url" /><Field label="API key" name="api_key" placeholder="sk-…" type="password" /><div className="form-row three"><Field label="Priority" name="priority" type="number" defaultValue="1" /><Field label="Weight" name="weight" type="number" defaultValue="1" /><Field label="Concurrency" name="max_concurrency" type="number" defaultValue="100" /></div><Field label="Cost weight" name="cost" type="number" defaultValue="0" step="0.001" /><FormActions pending={isPending} /></form></Modal>}
    {modal === 'model' && <Modal title="Create model route" onClose={() => setModal(null)}><form onSubmit={submitModel}><div className="form-row"><Field label="Public alias" name="alias" placeholder="gpt-fast" /><Field label="Upstream model" name="upstream_model" placeholder="gpt-4o-mini" /></div><fieldset><legend>Eligible accounts</legend>{data.accounts.map(account => <label className="check-row" key={account.id}><input type="checkbox" name="accounts" value={account.id} /><span>{account.display_name}</span><small>{account.name}</small></label>)}</fieldset><FormActions pending={isPending} /></form></Modal>}
    {modal === 'key' && <Modal title="Create virtual API key" onClose={() => setModal(null)}><form onSubmit={submitKey}><Field label="Key name" name="name" placeholder="cursor-local" /><Field label="Requests per minute" name="rpm" type="number" defaultValue="60" /><fieldset><legend>Allowed models</legend>{data.models.map(model => <label className="check-row" key={model.id}><input type="checkbox" name="models" value={model.alias} /><span>{model.alias}</span><small>{model.upstream_model}</small></label>)}</fieldset><FormActions pending={isPending} /></form></Modal>}
  </div>;
}

function ResourcePage({ eyebrow, title, description, action, children }: { eyebrow: string; title: string; description: string; action?: React.ReactNode; children: React.ReactNode }) {
  return <section className="page"><div className="page-heading compact"><div><span className="eyebrow">{eyebrow}</span><h1>{title}</h1><p>{description}</p></div><div className="heading-actions">{action}</div></div>{children}</section>;
}

function Empty({ title, body, onClick }: { title: string; body: string; onClick: () => void }) {
  return <button className="empty-state" onClick={onClick}><PlusIcon size={24} /><b>{title}</b><span>{body}</span></button>;
}

function Field({ label, name, type = 'text', placeholder, defaultValue, step }: { label: string; name: string; type?: string; placeholder?: string; defaultValue?: string; step?: string }) {
  return <label>{label}<input name={name} type={type} placeholder={placeholder} defaultValue={defaultValue} step={step} required /></label>;
}

function FormActions({ pending }: { pending: boolean }) {
  return <footer className="form-actions"><span>Changes create a new draft snapshot.</span><button className="primary" disabled={pending}>{pending ? 'Saving…' : 'Save changes'}</button></footer>;
}
