'use client';
import { useEffect, useMemo, useState } from 'react';
import { AlertFillIcon, CheckCircleFillIcon, DownloadIcon, SyncIcon } from '@primer/octicons-react';
import { discoverCodexModels, getDiscoveredModels, importDiscoveredModel, isValidPublicAlias, type DiscoveredModel, type DiscoveredModelStrategy, type DiscoveryResult, type DiscoveryScope } from '../codex-client';
import type { Translator } from '../i18n';

export function ModelDiscoveryModal({ accounts, providers, existingAliases, initialAccountId, t, onClose, onImported, onError }: {
  accounts: any[];
  providers: any[];
  existingAliases: string[];
  initialAccountId?: string;
  t: Translator;
  onClose: () => void;
  onImported: () => void | Promise<void>;
  onError: (error: unknown) => void;
}) {
  const discoveryProviders = useMemo(
    () => providers.filter(provider => provider.enabled && ['openai-compatible', 'openai-codex'].includes(provider.adapter)),
    [providers],
  );
  const discoveryProviderIds = useMemo(() => new Set(discoveryProviders.map(provider => provider.id)), [discoveryProviders]);
  const discoveryAccounts = useMemo(
    () => accounts.filter(account => account.enabled && discoveryProviderIds.has(account.provider_id)),
    [accounts, discoveryProviderIds],
  );
  const [scope, setScope] = useState(initialAccountId ? `account:${initialAccountId}` : 'all');
  const [models, setModels] = useState<DiscoveredModel[]>([]);
  const [results, setResults] = useState<DiscoveryResult[]>([]);
  const [selected, setSelected] = useState<Record<string, boolean>>({});
  const [aliases, setAliases] = useState<Record<string, string>>({});
  const [strategies, setStrategies] = useState<Record<string, DiscoveredModelStrategy>>({});
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState('');

  useEffect(() => {
    const closeOnEscape = (event: KeyboardEvent) => { if (event.key === 'Escape') onClose(); };
    window.addEventListener('keydown', closeOnEscape);
    return () => window.removeEventListener('keydown', closeOnEscape);
  }, [onClose]);

  async function loadModels() {
    const discovered = await getDiscoveredModels();
    setModels(discovered);
    setAliases(current => Object.fromEntries(discovered.map(model => { const key = `${model.provider_id}:${model.upstream_model}`; return [key, current[key] ?? model.upstream_model]; })));
  }

  useEffect(() => { void loadModels().catch(onError); }, []); // eslint-disable-line react-hooks/exhaustive-deps

  function toScope(): DiscoveryScope {
    if (scope === 'all') return { scope: 'all' };
    const [kind, id] = scope.split(':');
    return kind === 'provider' ? { scope: 'provider_id', provider_id: id } : { scope: 'account_id', account_id: id };
  }

  async function fetchModels() {
    setBusy(true); setMessage('');
    try {
      const response = await discoverCodexModels(toScope());
      setResults(response.results);
      await loadModels();
    } catch (error) { onError(error); }
    finally { setBusy(false); }
  }

  async function importSelected() {
    const chosen = models.filter(model => selected[`${model.provider_id}:${model.upstream_model}`]);
    if (!chosen.length) return;
    const foldedExisting = new Set(existingAliases.map(alias => alias.toLowerCase()));
    for (const model of chosen) {
      const key = `${model.provider_id}:${model.upstream_model}`;
      const alias = aliases[key]?.trim() ?? '';
      if (!isValidPublicAlias(alias)) { setMessage(t('codex.discovery.aliasInvalid', { alias: alias || model.upstream_model })); return; }
      if (foldedExisting.has(alias.toLowerCase())) { setMessage(t('codex.discovery.aliasConflict', { alias })); return; }
    }
    setBusy(true); setMessage('');
    try {
      for (const model of chosen) {
        const key = `${model.provider_id}:${model.upstream_model}`;
        await importDiscoveredModel({ alias: aliases[key].trim(), provider_id: model.provider_id, upstream_model: model.upstream_model, routing_strategy: strategies[key] ?? 'round_robin' });
      }
      setMessage(t('codex.discovery.imported', { count: chosen.length }));
      await onImported();
    } catch (error) { onError(error); }
    finally { setBusy(false); }
  }

  return (
    <div className="modal-backdrop" role="presentation" onMouseDown={onClose}>
      <section className="modal discovery-modal" role="dialog" aria-modal="true" aria-labelledby="model-discovery-title" onMouseDown={event => event.stopPropagation()}>
        <header><div><span className="eyebrow">{t('codex.discovery.eyebrow')}</span><h2 id="model-discovery-title">{t('codex.discovery.title')}</h2></div><button className="icon-button" onClick={onClose} aria-label={t('modal.close')}>×</button></header>
        <div className="discovery-toolbar">
          <label>{t('codex.discovery.scope')}<select value={scope} onChange={event => setScope(event.target.value)}><option value="all">{t('codex.discovery.all')}</option>{discoveryProviders.map(provider => <option key={provider.id} value={`provider:${provider.id}`}>{t('codex.discovery.provider', { name: provider.name })}</option>)}{discoveryAccounts.map(account => <option key={account.id} value={`account:${account.id}`}>{account.display_name}</option>)}</select></label>
          <button className="primary" onClick={() => void fetchModels()} disabled={busy}><SyncIcon size={15} />{busy ? t('codex.discovery.fetching') : t('codex.discovery.fetch')}</button>
        </div>
        {results.length > 0 && <div className="discovery-results">{results.map(result => <div key={result.account_id} className={result.status}><span>{result.status === 'succeeded' ? <CheckCircleFillIcon size={13} /> : <AlertFillIcon size={13} />}{result.account_name}</span><small>{result.status === 'succeeded' ? t('codex.discovery.modelsFound', { count: result.model_count ?? 0 }) : t('codex.discovery.failed')}</small></div>)}</div>}
        <div className="discovery-list">
          {models.map(model => {
            const key = `${model.provider_id}:${model.upstream_model}`;
            const availableIds = Object.entries(model.accounts).filter(([, state]) => state.available).map(([id]) => id);
            return <label className={`discovered-model ${model.available_account_count ? '' : 'unavailable'}`} key={key}>
              <input type="checkbox" checked={Boolean(selected[key])} disabled={!availableIds.length} onChange={event => setSelected(value => ({ ...value, [key]: event.target.checked }))} />
              <div><b>{model.display_name}</b><code>{model.upstream_model}</code><small>{model.provider_name} · {t('codex.discovery.availableAccounts', { count: model.available_account_count })}{model.metadata.image_generation ? ` · ${t('models.imageGeneration')}` : ''}{model.metadata.context_window ? ` · ${model.metadata.context_window}` : ''}</small></div>
              <div className="discovery-inputs"><input aria-label={t('form.publicAlias')} value={aliases[key] ?? model.upstream_model} onChange={event => setAliases(value => ({ ...value, [key]: event.target.value }))} disabled={!selected[key]} /><select aria-label={t('models.strategy')} value={strategies[key] ?? 'round_robin'} onChange={event => setStrategies(value => ({ ...value, [key]: event.target.value as DiscoveredModelStrategy }))} disabled={!selected[key]}><option value="round_robin">{t('models.strategyRoundRobin')}</option><option value="quota_failover">{t('models.strategyQuotaFailover')}</option><option value="p2c">{t('models.strategyP2C')}</option><option value="least_used">{t('models.strategyLeastUsed')}</option><option value="lkgp">{t('models.strategyLKGP')}</option><option value="reset_aware">{t('models.strategyResetAware')}</option></select></div>
            </label>;
          })}
          {!models.length && <div className="empty-inline">{t('codex.discovery.empty')}</div>}
        </div>
        {message && <div className="inline-state">{message}</div>}
        <footer className="form-actions"><span>{t('form.draftHint')}</span><button className="primary" onClick={() => void importSelected()} disabled={busy || !Object.values(selected).some(Boolean)}><DownloadIcon size={15} />{t('codex.discovery.importSelected')}</button></footer>
      </section>
    </div>
  );
}
