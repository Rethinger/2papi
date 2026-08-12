'use client';

import { GitBranchIcon, PencilIcon, TrashIcon } from '@primer/octicons-react';
import { capabilityValue, formatContextWindow, strategyLabel } from '../model-card';
import type { Locale, Translator } from '../i18n';

export function ModelCard({ model, locale, t, onEdit, onToggle, onDelete, onStrategy }: {
  model: any; locale: Locale; t: Translator;
  onEdit: () => void; onToggle: () => void; onDelete: () => void;
  onStrategy: (strategy: 'round_robin' | 'quota_failover') => void;
}) {
  const labels = { yes: t('models.yes'), no: t('models.no'), unknown: t('models.unknown') };
  const metadata = model.metadata ?? {};
  return <article className="model-card provider-model-card">
    <header><span className="provider-glyph tone-2"><GitBranchIcon size={16} /></span><span className={`status ${model.enabled ? 'good' : 'warn'}`}>{model.enabled ? t('models.available') : t('models.disabled')}</span></header>
    <h3>{model.alias}</h3><code>{model.upstream_model}</code>
    <div className="model-provider"><b>{model.provider_name ?? t('models.manualProvider')}</b><small>{model.adapter ?? t('models.manualRoute')}</small></div>
    {metadata.description && <p className="model-description">{metadata.description}</p>}
    <dl className="model-facts">
      <div><dt>{t('models.context')}</dt><dd>{formatContextWindow(metadata.context_window, locale, t('models.unknown'))}</dd></div>
      <div><dt>{t('models.tools')}</dt><dd>{capabilityValue(metadata.tools, labels)}</dd></div>
      <div><dt>{t('models.functions')}</dt><dd>{capabilityValue(metadata.function_calling, labels)}</dd></div>
      <div><dt>{t('models.reasoning')}</dt><dd>{capabilityValue(metadata.reasoning, labels)}</dd></div>
      <div><dt>{t('models.api')}</dt><dd>{capabilityValue(metadata.supported_in_api, labels)}</dd></div>
      <div><dt>{t('models.ownerTier')}</dt><dd>{[metadata.owner, metadata.tier].filter(Boolean).join(' · ') || t('models.unknown')}</dd></div>
    </dl>
    {model.provider_id ? <label className="model-strategy">{t('models.strategy')}<select value={model.routing_strategy} aria-label={t('models.strategy')} onChange={event => onStrategy(event.target.value as any)}><option value="round_robin">{strategyLabel('round_robin', locale)}</option><option value="quota_failover">{strategyLabel('quota_failover', locale)}</option></select></label> : <div className="route-chips">{(model.accounts ?? []).map((id: string) => <span key={id}>{id.slice(0, 8)}</span>)}</div>}
    <footer><span>{t('models.activeAccounts', { count: model.available_account_count ?? model.accounts?.length ?? 0 })}</span><div className="row-actions"><button className="ghost" onClick={onEdit}><PencilIcon size={13} />{t('action.edit')}</button><button className="ghost" onClick={onToggle}>{model.enabled ? t('action.disable') : t('action.enable')}</button><button className="danger-quiet" onClick={onDelete}><TrashIcon size={13} />{t('action.delete')}</button></div></footer>
  </article>;
}
