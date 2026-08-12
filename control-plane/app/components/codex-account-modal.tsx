'use client';

import { BrowserIcon, CheckIcon, CopyIcon, FileIcon, KeyIcon } from '@primer/octicons-react';
import { useCallback, useEffect, useRef, useState } from 'react';
import type { Translator } from '../i18n';
import {
  CODEX_AUTH_CHANNEL,
  MAX_AUTH_FILE_BYTES,
  getCodexDeviceStatus,
  importCodexAuth,
  openCodexAuthWindow,
  patchAccount,
  startCodexBrowser,
  startCodexDevice,
  type CodexAuthMethod,
  type DeviceStart,
} from '../codex-client';

type RoutingDefaults = { displayName: string; priority: number; maxConcurrency: number };

export function CodexAccountModal({ t, onClose, onConnected, onError }: {
  t: Translator;
  onClose: () => void;
  onConnected: (accountId: string) => void | Promise<void>;
  onError: (error: unknown) => void;
}) {
  const [tab, setTab] = useState<CodexAuthMethod>('browser');
  const [busy, setBusy] = useState(false);
  const [browserWaiting, setBrowserWaiting] = useState(false);
  const [device, setDevice] = useState<DeviceStart | null>(null);
  const [deviceState, setDeviceState] = useState('');
  const [fileName, setFileName] = useState('');
  const [fileContents, setFileContents] = useState('');
  const [localError, setLocalError] = useState('');
  const [routing, setRouting] = useState<RoutingDefaults>({ displayName: '', priority: 1, maxConcurrency: 1 });
  const polling = useRef(false);

  useEffect(() => {
    const closeOnEscape = (event: KeyboardEvent) => { if (event.key === 'Escape') onClose(); };
    window.addEventListener('keydown', closeOnEscape);
    return () => window.removeEventListener('keydown', closeOnEscape);
  }, [onClose]);

  const finish = useCallback(async (accountId: string) => {
    const changes: Record<string, unknown> = {
      priority: routing.priority,
      max_concurrency: routing.maxConcurrency,
    };
    if (routing.displayName.trim()) changes.display_name = routing.displayName.trim();
    await patchAccount(accountId, changes);
    await onConnected(accountId);
  }, [onConnected, routing]);

  useEffect(() => {
    const channel = new BroadcastChannel(CODEX_AUTH_CHANNEL);
    channel.onmessage = event => {
      const message = event.data as { status?: string; accountId?: string };
      if (message.status === 'connected' && message.accountId) {
        setBusy(true);
        void finish(message.accountId).catch(onError).finally(() => setBusy(false));
      } else if (message.status && message.status !== 'connected') {
        setLocalError(t('codex.error.browser'));
      }
    };
    return () => channel.close();
  }, [finish, onError, t]);

  useEffect(() => {
    if (!device || polling.current) return;
    polling.current = true;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const poll = async () => {
      try {
        const status = await getCodexDeviceStatus(device.session);
        if (cancelled) return;
        setDeviceState(status.state);
        if (status.state === 'complete' && status.account_id) {
          await finish(status.account_id);
          return;
        }
        if (status.state === 'expired' || status.state === 'denied') return;
        timer = setTimeout(poll, Math.max(2, status.interval ?? device.interval) * 1000);
      } catch (error) {
        if (!cancelled) {
          setDeviceState('failed');
          timer = setTimeout(poll, Math.max(5, device.interval) * 1000);
        }
      }
    };
    timer = setTimeout(poll, Math.max(1, device.interval) * 1000);
    return () => { cancelled = true; polling.current = false; if (timer) clearTimeout(timer); };
  }, [device, finish]);

  async function browserLogin() {
    setBusy(true); setLocalError('');
    try {
      const started = await startCodexBrowser(routing.displayName.trim() || undefined);
      if (!openCodexAuthWindow(started.authorization_url)) throw new Error('popup_blocked');
      setBrowserWaiting(true);
    } catch (error) { setLocalError(t('codex.error.browser')); onError(error); }
    finally { setBusy(false); }
  }

  async function deviceLogin() {
    setBusy(true); setLocalError(''); setDeviceState('pending');
    try { setDevice(await startCodexDevice()); }
    catch (error) { setLocalError(t('codex.error.device')); onError(error); }
    finally { setBusy(false); }
  }

  async function importFile() {
    if (!fileContents) return;
    setBusy(true); setLocalError('');
    try {
      const result = await importCodexAuth(fileContents);
      setFileContents('');
      await finish(result.account_id);
    } catch (error) { setLocalError(t('codex.error.import')); onError(error); }
    finally { setBusy(false); }
  }

  async function selectFile(file?: File) {
    setFileContents(''); setFileName(''); setLocalError('');
    if (!file) return;
    if (file.size > MAX_AUTH_FILE_BYTES) { setLocalError(t('codex.error.fileTooLarge')); return; }
    setFileName(file.name);
    setFileContents(await file.text());
  }

  return (
    <div className="modal-backdrop" role="presentation" onMouseDown={onClose}>
      <section className="modal codex-modal" role="dialog" aria-modal="true" aria-labelledby="codex-account-title" onMouseDown={event => event.stopPropagation()}>
        <header><div><span className="eyebrow">{t('codex.modal.eyebrow')}</span><h2 id="codex-account-title">{t('codex.modal.title')}</h2></div><button className="icon-button" onClick={onClose} aria-label={t('modal.close')}>×</button></header>
        <div className="codex-mark"><span><KeyIcon size={18} /></span><div><b>{t('codex.modal.secureTitle')}</b><small>{t('codex.modal.secureBody')}</small></div></div>
        <div className="auth-tabs" role="tablist">
          {([['browser', BrowserIcon, 'codex.tab.browser'], ['device', KeyIcon, 'codex.tab.device'], ['import', FileIcon, 'codex.tab.import']] as const).map(([id, Icon, label]) => (
            <button key={id} type="button" role="tab" aria-selected={tab === id} className={tab === id ? 'active' : ''} onClick={() => { setTab(id); setLocalError(''); }}><Icon size={15} />{t(label)}</button>
          ))}
        </div>
        <div className="codex-routing-fields">
          <label>{t('form.displayName')}<input value={routing.displayName} onChange={event => setRouting(value => ({ ...value, displayName: event.target.value }))} placeholder={t('codex.form.namePlaceholder')} /></label>
          <div className="form-row">
            <NumberField label={t('form.priority')} value={routing.priority} onChange={priority => setRouting(value => ({ ...value, priority }))} />
            <NumberField label={t('form.concurrency')} value={routing.maxConcurrency} min={1} onChange={maxConcurrency => setRouting(value => ({ ...value, maxConcurrency }))} />
          </div>
        </div>
        <div className="auth-panel">
          {tab === 'browser' && <>
            <p>{t('codex.browser.description')}</p>
            {browserWaiting ? <div className="inline-state good"><CheckIcon size={16} /><span>{t('codex.browser.waiting')}</span></div> : <button className="primary wide" onClick={() => void browserLogin()} disabled={busy}><BrowserIcon size={16} />{t('codex.browser.action')}</button>}
            <small>{t('codex.browser.fallback')}</small>
          </>}
          {tab === 'device' && <>
            <p>{t('codex.device.description')}</p>
            {!device ? <button className="primary wide" onClick={() => void deviceLogin()} disabled={busy}><KeyIcon size={16} />{t('codex.device.action')}</button> : <div className="device-code-card">
              <span>{t('codex.device.code')}</span><strong>{device.user_code}</strong>
              <div><button className="secondary" onClick={() => navigator.clipboard.writeText(device.user_code)}><CopyIcon size={14} />{t('action.copy')}</button><a className="primary" href={device.verification_uri} target="_blank" rel="noreferrer">{t('codex.device.open')}</a></div>
              <small>{t(`codex.device.state.${deviceState || 'pending'}` as Parameters<Translator>[0])}</small>
            </div>}
          </>}
          {tab === 'import' && <>
            <p>{t('codex.import.description')}</p>
            <label className="file-drop"><FileIcon size={20} /><b>{fileName || t('codex.import.choose')}</b><span>{t('codex.import.limit')}</span><input type="file" accept="application/json,.json" onChange={event => void selectFile(event.target.files?.[0])} /></label>
            <button className="primary wide" onClick={() => void importFile()} disabled={busy || !fileContents}>{t('codex.import.action')}</button>
          </>}
        </div>
        {localError && <div className="inline-error">{localError}</div>}
      </section>
    </div>
  );
}

function NumberField({ label, value, min, onChange }: { label: string; value: number; min?: number; onChange: (value: number) => void }) {
  return <label>{label}<input type="number" min={min} value={value} onChange={event => onChange(Number(event.target.value))} /></label>;
}
