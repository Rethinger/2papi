'use client';

import { BrowserIcon, KeyIcon } from '@primer/octicons-react';
import { useCallback, useEffect, useRef, useState } from 'react';
import type { Translator } from '../i18n';

export const CLAUDE_AUTH_CHANNEL = '2papi-claude-auth';

export type ClaudeCredentialKind = 'browser' | 'oauth' | 'cookie';

type ClaudeRouting = { displayName: string; priority: number; maxConcurrency: number };

export class ClaudeClientError extends Error {
  code: string;
  status: number;
  constructor(code: string, message: string, status: number) {
    super(message);
    this.name = 'ClaudeClientError';
    this.code = code;
    this.status = status;
  }
}

async function claudeRequest<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`/api/control/v1/${path}`, init);
  const payload = await response.json().catch(() => ({})) as Record<string, unknown>;
  if (!response.ok) {
    const structured = payload.error && typeof payload.error === 'object' ? payload.error as Record<string, unknown> : null;
    const code = String(structured?.code ?? payload.error ?? 'claude_request_failed');
    const message = String(structured?.message ?? code);
    throw new ClaudeClientError(code, message, response.status);
  }
  return (Object.prototype.hasOwnProperty.call(payload, 'data') ? payload.data : payload) as T;
}

const CLAUDE_BASE_URL = 'https://claude.ai';

export type ClaudeProviderRow = { id: string; adapter: string; base_url: string };

// ensureClaudeProvider finds or creates the anthropic provider used by
// claude.ai cookie/OAuth accounts (API-key accounts may use any base URL).
async function ensureClaudeProvider(): Promise<string> {
  const providers = await claudeRequest<ClaudeProviderRow[]>('providers');
  const existing = providers.find(provider => provider.adapter === 'anthropic' && provider.base_url === CLAUDE_BASE_URL);
  if (existing) return existing.id;
  const created = await claudeRequest<ClaudeProviderRow>('providers', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ slug: 'claude', name: 'Claude', adapter: 'anthropic', base_url: CLAUDE_BASE_URL, enabled: true, metadata: {} }),
  });
  return created.id;
}

export async function createClaudeAccount(input: {
  kind: ClaudeCredentialKind;
  cookies?: string;
  accessToken?: string;
  organizationId?: string;
  displayName: string;
  priority: number;
  maxConcurrency: number;
}): Promise<{ id: string }> {
  const providerId = await ensureClaudeProvider();
  const credential: Record<string, unknown> = { kind: input.kind };
  if (input.kind === 'cookie') {
    credential.cookies = input.cookies;
    credential.organization_id = input.organizationId || undefined;
  }
  if (input.kind === 'oauth') {
    credential.access_token = input.accessToken;
    credential.organization_id = input.organizationId || undefined;
  }
  const name = input.displayName.trim() ? `claude-${input.displayName.trim().toLowerCase().replace(/[^a-z0-9._-]+/g, '-')}` : `claude-${crypto.randomUUID().slice(0, 12)}`;
  return claudeRequest<{ id: string }>('accounts', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      provider_id: providerId,
      name,
      display_name: input.displayName.trim() || 'Claude account',
      base_url: CLAUDE_BASE_URL,
      enabled: true,
      priority: input.priority,
      weight: 1,
      max_concurrency: input.maxConcurrency,
      cost: 0,
      credential,
      metadata: { auth_method: input.kind },
    }),
  });
}

function NumberField({ label, value, min, onChange }: { label: string; value: number; min?: number; onChange: (value: number) => void }) {
  return <label>{label}<input type="number" min={min} value={value} onChange={event => onChange(Number(event.target.value))} /></label>;
}

export function ClaudeAccountModal({ t, onClose, onConnected, onError }: {
  t: Translator;
  onClose: () => void;
  onConnected: (accountId: string) => void | Promise<void>;
  onError: (error: unknown) => void;
}) {
  const [tab, setTab] = useState<ClaudeCredentialKind>('browser');
  const [busy, setBusy] = useState(false);
  const [browserWaiting, setBrowserWaiting] = useState(false);
  const [localError, setLocalError] = useState('');
  const [routing, setRouting] = useState<ClaudeRouting>({ displayName: '', priority: 1, maxConcurrency: 1 });
  const [cookies, setCookies] = useState('');
  const [accessToken, setAccessToken] = useState('');
  const [organizationId, setOrganizationId] = useState('');

  const finish = useCallback(async (accountId: string) => {
    setBusy(true);
    try { await onConnected(accountId); }
    finally { setBusy(false); }
  }, [onConnected]);

  useEffect(() => {
    const channel = new BroadcastChannel(CLAUDE_AUTH_CHANNEL);
    channel.onmessage = event => {
      const data = event.data as { status?: string; account_id?: string };
      if (data.status === 'connected' && data.account_id) void finish(data.account_id);
    };
    return () => channel.close();
  }, [finish]);

  async function browserLogin() {
    setBusy(true);
    setLocalError('');
    try {
      const started = await claudeRequest<{ authorization_url: string }>('claude/oauth/start', { method: 'POST', body: '{}' });
      const popup = window.open(started.authorization_url, '2papi-claude-auth', 'popup=yes,width=520,height=680');
      if (!popup) throw new Error('popup_blocked');
      setBrowserWaiting(true);
    } catch (error) {
      setLocalError(t('claude.error.browser'));
      onError(error);
    } finally {
      setBusy(false);
    }
  }

  async function submit() {
    setBusy(true);
    setLocalError('');
    try {
      const result = await createClaudeAccount({
        kind: tab as 'oauth' | 'cookie',
        cookies,
        accessToken,
        organizationId,
        displayName: routing.displayName,
        priority: routing.priority,
        maxConcurrency: routing.maxConcurrency,
      });
      await onConnected(result.id);
    } catch (error) {
      setLocalError(error instanceof Error ? error.message : String(error));
      onError(error);
    } finally {
      setBusy(false);
    }
  }

  const canSubmit = (tab === 'cookie' && cookies.trim()) || (tab === 'oauth' && accessToken.trim());

  return (
    <div className="modal-backdrop" role="presentation" onMouseDown={onClose}>
      <section className="modal codex-modal" role="dialog" aria-modal="true" aria-labelledby="claude-account-title" onMouseDown={event => event.stopPropagation()}>
        <header><div><span className="eyebrow">Claude</span><h2 id="claude-account-title">{t('claude.modal.title')}</h2></div><button className="icon-button" onClick={onClose} aria-label={t('modal.close')}>×</button></header>
        <div className="codex-mark"><span><KeyIcon size={18} /></span><div><b>{t('codex.modal.secureTitle')}</b><small>{t('claude.modal.secureBody')}</small></div></div>
        <div className="auth-tabs" role="tablist">
          {(['browser', 'oauth', 'cookie'] as const).map(method => (
            <button key={method} type="button" role="tab" aria-selected={tab === method} className={tab === method ? 'active' : ''} onClick={() => { setTab(method); setLocalError(''); }}>
              {t(({ browser: 'claude.tab.browser', oauth: 'credential.kind.oauth', cookie: 'credential.kind.cookie' } as const)[method])}
            </button>
          ))}
        </div>
        <div className="codex-routing-fields">
          <label>{t('form.displayName')}<input value={routing.displayName} onChange={event => setRouting(value => ({ ...value, displayName: event.target.value }))} placeholder={t('claude.form.namePlaceholder')} /></label>
          <div className="form-row">
            <NumberField label={t('form.priority')} value={routing.priority} onChange={priority => setRouting(value => ({ ...value, priority }))} />
            <NumberField label={t('form.concurrency')} value={routing.maxConcurrency} min={1} onChange={maxConcurrency => setRouting(value => ({ ...value, maxConcurrency }))} />
          </div>
        </div>
        <div className="auth-panel">
          {tab === 'browser' && <>
            <p>{t('claude.browser.description')}</p>
            {browserWaiting ? <div className="inline-state good"><KeyIcon size={16} /><span>{t('claude.browser.waiting')}</span></div> : <button className="primary wide" onClick={() => void browserLogin()} disabled={busy}><BrowserIcon size={16} />{t('claude.browser.action')}</button>}
            <small>{t('claude.browser.fallback')}</small>
          </>}
          {tab === 'cookie' && <>
            <label>{t('form.cookies')}<textarea rows={3} value={cookies} onChange={event => setCookies(event.target.value)} placeholder="sessionKey=sk-ant-…" /></label>
            <label>{t('form.organizationId')}<input value={organizationId} onChange={event => setOrganizationId(event.target.value)} placeholder={t('form.organizationOptional')} /></label>
            <small>{t('claude.cookie.description')}</small>
          </>}
          {tab === 'oauth' && <>
            <label>{t('form.accessToken')}<input type="password" value={accessToken} onChange={event => setAccessToken(event.target.value)} placeholder="sk-ant-…" /></label>
            <label>{t('form.organizationId')}<input value={organizationId} onChange={event => setOrganizationId(event.target.value)} placeholder={t('form.organizationOptional')} /></label>
            <small>{t('claude.oauth.description')}</small>
          </>}
          {localError && <div className="inline-state warn">{localError}</div>}
        </div>
        <footer className="form-actions">
          <span>{t('form.draftHint')}</span>
          <button className="primary" onClick={() => void (tab === 'browser' ? browserLogin() : submit())} disabled={busy || (tab !== 'browser' && !canSubmit)}>{tab === 'browser' ? t('claude.browser.action') : t('claude.modal.connect')}</button>
        </footer>
      </section>
    </div>
  );
}
