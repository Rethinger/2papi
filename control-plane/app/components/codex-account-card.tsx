'use client';

import { CheckCircleFillIcon, KeyIcon, PencilIcon, SyncIcon, TrashIcon } from '@primer/octicons-react';
import { useState } from 'react';
import { codexPlanLabel, openCodexAuthWindow, reauthorizeCodexAccount } from '../codex-client';
import type { Translator } from '../i18n';
import { CodexQuotaPanel } from './codex-quota-panel';

export function CodexAccountCard({ account, t, onChanged, onDiscover, onToggle, onEdit, onDelete, onError }: {
  account: any;
  t: Translator;
  onChanged: () => void | Promise<void>;
  onDiscover: () => void;
  onToggle: () => void;
  onEdit: () => void;
  onDelete: () => void;
  onError: (error: unknown) => void;
}) {
  const [busy, setBusy] = useState(false);
  const authMethod = ['browser', 'device', 'import'].includes(account.metadata?.auth_method) ? account.metadata.auth_method : 'unknown';

  async function reauthorize() {
    setBusy(true);
    try {
      const result = await reauthorizeCodexAccount(account.id);
      if (!openCodexAuthWindow(result.authorization_url)) throw new Error('popup_blocked');
      await onChanged();
    } catch (error) { onError(error); }
    finally { setBusy(false); }
  }

  return (
    <article className="codex-account-card">
      <header>
        <div className="account-title"><span className="provider-glyph codex-glyph"><KeyIcon size={16} /></span><div><b>{account.display_name}</b><small>{account.account_email || account.name}</small></div></div>
        <span className={`status ${account.enabled ? '' : 'warn'}`}><CheckCircleFillIcon size={12} />{account.enabled ? t('accounts.enabled') : t('accounts.disabled')}</span>
      </header>
      <div className="codex-account-facts">
        <div><span>{t('codex.account.plan')}</span><b>{codexPlanLabel(account.plan_type, t('codex.account.planUnknown'))}</b></div>
        <div><span>{t('codex.account.auth')}</span><b>{t(`codex.auth.${authMethod}` as Parameters<Translator>[0])}</b></div>
        <div><span>{t('codex.account.revision')}</span><b>v{account.credential_revision ?? 1}</b></div>
      </div>
      <CodexQuotaPanel account={account} t={t} />
      <footer>
        <button className="ghost" onClick={onDiscover}><SyncIcon size={14} />{t('codex.account.fetchModels')}</button>
        <button className="ghost" onClick={() => void reauthorize()} disabled={busy}>{t('codex.account.reauthorize')}</button>
        <button className="ghost" onClick={onEdit}><PencilIcon size={13} />{t('action.edit')}</button>
        <button className="ghost" onClick={onToggle}>{account.enabled ? t('action.disable') : t('action.enable')}</button>
        <button className="danger-quiet" onClick={onDelete}><TrashIcon size={13} />{t('action.delete')}</button>
      </footer>
    </article>
  );
}
