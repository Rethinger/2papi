'use client';

import { AlertIcon, ClockIcon, LinkExternalIcon, SyncIcon } from '@primer/octicons-react';
import { useCallback, useEffect, useState } from 'react';
import { clampQuotaPercent, getCodexQuota, reconcileCodexQuotaReset, refreshCodexQuota, resetCodexQuota, resolveCodexQuotaReset, splitRemaining, type CodexQuotaState, type CodexQuotaWindow, type CodexResetOperation } from '../codex-client';
import { dateLocale, type Locale, type Translator } from '../i18n';

// Light DB poll while the panel is visible; upstream refresh only when the
// cached usage snapshot is older than this, so ChatGPT APIs are not hammered.
const QUOTA_POLL_MS = 60_000;
const QUOTA_STALE_MS = 5 * 60_000;

type CodexAccount = { id: string; token_expires_at?: string | null };

export function CodexQuotaPanel({ account, locale, t }: { account: CodexAccount; locale: Locale; t: Translator }) {
  const expiresAt = account.token_expires_at ? new Date(account.token_expires_at) : null;
  const expired = expiresAt ? expiresAt.getTime() <= Date.now() : false;
  const [state, setState] = useState<CodexQuotaState | null>(null);
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState(false);
  const [confirmReset, setConfirmReset] = useState(false);
  const [resetOperation, setResetOperation] = useState<CodexResetOperation | null>(null);
  const [idempotencyKey, setIdempotencyKey] = useState('');
  const [resolutionNote, setResolutionNote] = useState('');

  const load = useCallback(async (refresh: boolean) => {
    setBusy(true);
    setFailed(false);
    try {
      const loaded = refresh ? await refreshCodexQuota(account.id) : await getCodexQuota(account.id);
      setState(loaded);
      setResetOperation(loaded.reset_operation ?? null);
      if (!refresh && quotaNeedsUpstreamRefresh(loaded)) {
        try {
          const fresh = await refreshCodexQuota(account.id);
          setState(fresh);
          setResetOperation(fresh.reset_operation ?? null);
        } catch {
          // Cached copy stays visible; the refresh button still offers a manual retry.
        }
      }
    } catch {
      setFailed(true);
    } finally {
      setBusy(false);
    }
  }, [account.id]);

  useEffect(() => { void load(false); }, [load]);

  useEffect(() => {
    if (typeof document === 'undefined') return;
    const tick = () => { if (document.visibilityState !== 'hidden') void load(false); };
    const onVisible = () => { if (document.visibilityState === 'visible') void load(false); };
    const id = window.setInterval(tick, QUOTA_POLL_MS);
    document.addEventListener('visibilitychange', onVisible);
    return () => {
      window.clearInterval(id);
      document.removeEventListener('visibilitychange', onVisible);
    };
  }, [load]);

  const primary = state?.quota.rate_limit?.primary_window;
  const secondary = state?.quota.rate_limit?.secondary_window;
  const countdownActive = Boolean(primary?.reset_at && primary.reset_at * 1000 > Date.now() + 5_000);
  const now = useTickingNow(countdownActive, 1000);
  const unsupported = state?.capability_status === 'unsupported' || state?.capability_status === 'contract_changed';
  const erred = state != null && state.capability_status === 'error';
  const creditCount = state?.reset_credits.available_count ?? 0;
  const creditExpiry = state?.reset_credits.next_expires_at ? new Date(state.reset_credits.next_expires_at) : null;
  const fetchedAt = quotaFetchedAt(state);

  const beginReset = () => {
    setIdempotencyKey(crypto.randomUUID());
    setConfirmReset(true);
  };

  const performReset = async () => {
    setConfirmReset(false);
    setBusy(true);
    setFailed(false);
    try {
      const operation = await resetCodexQuota(account.id, idempotencyKey);
      setResetOperation(operation);
      if (operation.status === 'succeeded') await load(true);
    } catch {
      setFailed(true);
    } finally {
      setBusy(false);
    }
  };

  const reconcile = async () => {
    if (!resetOperation) return;
    setBusy(true);
    try {
      const operation = await reconcileCodexQuotaReset(account.id, resetOperation.id);
      setResetOperation(operation);
      if (operation.status !== 'unknown') await load(true);
    } catch { setFailed(true); } finally { setBusy(false); }
  };

  const resolve = async (resolution: 'succeeded' | 'failed') => {
    if (!resetOperation) return;
    setBusy(true);
    try {
      await resolveCodexQuotaReset(account.id, resetOperation.id, resolution, resolutionNote);
      setResolutionNote('');
      await load(true);
    } catch { setFailed(true); } finally { setBusy(false); }
  };
  return (
    <div className="codex-quota-panel">
      <div><ClockIcon size={14} /><span>{expiresAt ? t(expired ? 'codex.token.expired' : 'codex.token.expires', { date: expiresAt.toLocaleString() }) : t('codex.token.unknown')}</span></div>
      {primary ? <>
        <QuotaWindow label={t('codex.quota.primary')} window={primary} t={t} now={now} />
        {secondary && <QuotaWindow label={t('codex.quota.secondary')} window={secondary} t={t} now={now} />}
        <div className="quota-credit-row"><span>{t('codex.quota.resetCredits', { count: creditCount })}</span></div>
        {creditExpiry && <small>{t('codex.quota.creditExpires', { date: creditExpiry.toLocaleString() })}</small>}
        {state?.local_usage && <small className="quota-local">{t('codex.quota.localTraffic', { tokens: formatTokens(state.local_usage.tokens, locale), requests: state.local_usage.requests })}</small>}
        {fetchedAt && <small className="quota-updated">{formatUpdatedAge(Math.max(0, Date.now() - fetchedAt.getTime()), t)}</small>}
      </> : (
        <div className="quota-placeholder"><span>{t('codex.quota.title')}</span><b>{erred ? t('codex.quota.failed') : unsupported ? t('codex.quota.unsupported') : t('codex.quota.unavailable')}</b></div>
      )}
      <div className="quota-actions">
        <button className="ghost" onClick={() => void load(true)} disabled={busy}><SyncIcon size={13} />{busy ? t('codex.quota.refreshing') : t('codex.quota.refresh')}</button>
        {creditCount > 0 && !resetOperation && <button className="danger-quiet" onClick={beginReset} disabled={busy}>{t('codex.quota.reset')}</button>}
        <a href="https://chatgpt.com/codex/settings/usage" target="_blank" rel="noreferrer"><LinkExternalIcon size={13} />{t('codex.quota.openUsage')}</a>
      </div>
      {resetOperation?.status === 'pending' && <div className="quota-operation-note"><SyncIcon size={14} />{t('codex.quota.resetPending')}</div>}
      {resetOperation?.status === 'failed' && <div className="quota-operation-note danger"><AlertIcon size={14} />{t('codex.quota.resetFailed')}</div>}
      {resetOperation?.status === 'unknown' && <div className="quota-resolution">
        <div className="quota-operation-note warning"><AlertIcon size={14} />{t('codex.quota.resetUnknown')}</div>
        <button className="ghost" onClick={() => void reconcile()} disabled={busy}><SyncIcon size={13} />{t('codex.quota.reconcile')}</button>
        <p>{t('codex.quota.resolveWarning')}</p>
        <textarea value={resolutionNote} onChange={event => setResolutionNote(event.target.value)} placeholder={t('codex.quota.resolveNote')} maxLength={1000} />
        <div className="quota-resolution-actions">
          <button className="ghost" onClick={() => void resolve('failed')} disabled={busy || resolutionNote.trim().length < 10}>{t('codex.quota.resolveFailed')}</button>
          <button className="danger-quiet" onClick={() => void resolve('succeeded')} disabled={busy || resolutionNote.trim().length < 10}>{t('codex.quota.resolveSucceeded')}</button>
        </div>
      </div>}
      {confirmReset && <div className="quota-confirm-backdrop" role="presentation" onMouseDown={event => { if (event.target === event.currentTarget) setConfirmReset(false); }}>
        <div className="quota-confirm" role="dialog" aria-modal="true" aria-labelledby={`quota-reset-title-${account.id}`}>
          <AlertIcon size={20} />
          <h4 id={`quota-reset-title-${account.id}`}>{t('codex.quota.resetConfirmTitle')}</h4>
          <p>{t('codex.quota.resetConfirmBody')}</p>
          <div><button className="ghost" onClick={() => setConfirmReset(false)}>{t('codex.quota.resetCancel')}</button><button className="danger" onClick={() => void performReset()}>{t('codex.quota.resetConfirmAction')}</button></div>
        </div>
      </div>}
    </div>
  );
}

function QuotaWindow({ label, window, t, now }: { label: string; window: CodexQuotaWindow; t: Translator; now: number }) {
  const used = clampQuotaPercent(window.used_percent);
  const reset = window.reset_at > 0 ? new Date(window.reset_at * 1000) : null;
  const remainingMs = reset ? reset.getTime() - now : 0;
  const countdown = reset && remainingMs > 0 ? t('codex.quota.resetsIn', { time: formatRemaining(remainingMs, t) }) : null;
  return <div className="codex-quota-window">
    <div><span>{label}</span><b>{t('codex.quota.used', { percent: Math.round(used * 10) / 10 })}</b></div>
    <div className="quota"><span style={{ width: `${used}%` }} /></div>
    {countdown && <small>{countdown}</small>}
    {!countdown && reset && <small>{t('codex.quota.resets', { date: reset.toLocaleString() })}</small>}
  </div>;
}

function useTickingNow(active: boolean, intervalMs: number) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    const id = window.setInterval(() => setNow(Date.now()), intervalMs);
    return () => window.clearInterval(id);
  }, [active, intervalMs]);
  return now;
}

function quotaFetchedAt(state: CodexQuotaState | null): Date | null {
  if (!state) return null;
  const raw = state.quota?.fetched_at ?? state.fetched_at ?? null;
  if (!raw) return null;
  const time = new Date(raw).getTime();
  return Number.isFinite(time) ? new Date(time) : null;
}

function quotaNeedsUpstreamRefresh(state: CodexQuotaState | null) {
  if (!state) return false;
  if (state.capability_status === 'unsupported' || state.capability_status === 'contract_changed' || state.capability_status === 'error') return false;
  const fetched = quotaFetchedAt(state);
  if (fetched === null) return state.capability_status === 'unknown';
  return Date.now() - fetched.getTime() > QUOTA_STALE_MS;
}

function formatRemaining(ms: number, t: Translator) {
  const { hours, minutes, seconds } = splitRemaining(ms);
  if (hours > 0) return t('codex.quota.timeHoursMinutes', { hours, minutes });
  if (minutes > 0) return t('codex.quota.timeMinutesSeconds', { minutes, seconds });
  return t('codex.quota.timeSeconds', { seconds });
}

function formatTokens(value: number, locale: Locale) {
  return new Intl.NumberFormat(dateLocale(locale), { notation: 'compact', maximumFractionDigits: 1 }).format(value);
}

function formatUpdatedAge(ms: number, t: Translator) {
  if (ms < 60_000) return t('codex.quota.updatedJustNow');
  return t('codex.quota.updated', { duration: t('codex.quota.durationMinutes', { n: Math.floor(ms / 60_000) }) });
}
