'use client';

// Billing view (шаг 6, Cloud): operator-facing balances + credit ledger +
// manual adjustment. Money path — adjustments always ask for confirmation
// and land in the ledger (source of truth) server-side.

import { useState } from 'react';

export interface BillingViewProps {
  billing: { checkout_url?: string; configured?: boolean; balances: any[]; transactions: any[] } | null;
  locale: string;
  t: (key: string, vars?: Record<string, unknown>) => string;
  onAdjusted: () => void;
}

const dateLocale = (locale: string) => (locale === 'ru' ? 'ru-RU' : 'en-US');

function fmtUsd(value: unknown): string {
  const n = Number(value ?? 0);
  return `${n < 0 ? '-' : ''}$${Math.abs(n).toFixed(4)}`;
}

export function BillingView({ billing, locale, t, onAdjusted }: BillingViewProps) {
  const [teamId, setTeamId] = useState('');
  const [delta, setDelta] = useState('');
  const [note, setNote] = useState('');
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<string | null>(null);

  if (!billing) {
    return <ResourceFrame t={t}><p className="muted">…</p></ResourceFrame>;
  }

  const balances: any[] = billing.balances ?? [];
  const transactions: any[] = billing.transactions ?? [];

  const submitAdjustment = async () => {
    const parsed = Number(delta);
    const team = balances.find(b => b.id === teamId);
    if (!team || !Number.isFinite(parsed) || parsed === 0) {
      setMessage(t('billing.adjustInvalid'));
      return;
    }
    const sign = parsed > 0 ? '+' : '';
    const confirmed = window.confirm(t('billing.adjustConfirm', {
      team: team.name,
      amount: `${sign}${fmtUsd(parsed)}`,
      note: note || '—',
    }));
    if (!confirmed) return;
    setBusy(true);
    setMessage(null);
    try {
      const res = await fetch('/api/control/v1/billing/adjust', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ team_id: teamId, delta_usd: parsed, note }),
      });
      const body = await res.json();
      if (!res.ok) throw new Error(body?.error?.message ?? t('billing.adjustFailed'));
      setMessage(t('billing.adjustDone', { team: team.name, amount: `${sign}${fmtUsd(parsed)}` }));
      setDelta('');
      setNote('');
      onAdjusted();
    } catch (cause: any) {
      setMessage(cause?.message ?? t('billing.adjustFailed'));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div>
      <h2 className="page-title">{t('billing.title')}</h2>
      <p className="muted">{t('billing.subtitle')}</p>

      {!billing.configured && (
        <p className="callout warn">{t('billing.webhookNotConfigured')}</p>
      )}

      <section className="panel">
        <h3>{t('balances.title')}</h3>
        <div className="table-card">
          <table>
            <thead><tr>
              <th>{t('balances.team')}</th>
              <th>{t('balances.balance')}</th>
              <th>{t('balances.dailyBudget')}</th>
              <th>{t('balances.credited')}</th>
              <th>{t('balances.debited')}</th>
            </tr></thead>
            <tbody>
              {balances.map(b => (
                <tr key={b.id}>
                  <td><b>{b.name}</b></td>
                  <td className={Number(b.balance_usd) <= 0 ? 'danger' : ''}>{fmtUsd(b.balance_usd)}</td>
                  <td>{Number(b.budget_usd) > 0 ? `$${Number(b.budget_usd).toFixed(2)}/day` : '∞'}</td>
                  <td>{fmtUsd(b.credited_usd)}</td>
                  <td>{fmtUsd(b.debited_usd)}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {!balances.length && <EmptyState label={t('balances.empty')} />}
        </div>
      </section>

      <section className="panel">
        <h3>{t('adjust.title')}</h3>
        {!billing.checkout_url && <p className="muted">{t('adjust.noCheckoutHint')}</p>}
        {billing.checkout_url && (
          <p><a href={billing.checkout_url} target="_blank" rel="noreferrer">{t('adjust.openCheckout')}</a></p>
        )}
        <div className="form-row">
          <select value={teamId} onChange={e => setTeamId(e.target.value)} aria-label={t('adjust.team')}>
            <option value="">{t('adjust.pickTeam')}</option>
            {balances.map(b => <option key={b.id} value={b.id}>{b.name}</option>)}
          </select>
          <input
            type="number" step="0.01" placeholder={t('adjust.deltaPlaceholder')}
            value={delta} onChange={e => setDelta(e.target.value)} aria-label={t('adjust.deltaPlaceholder')}
          />
          <input
            type="text" placeholder={t('adjust.notePlaceholder')}
            value={note} onChange={e => setNote(e.target.value)} aria-label={t('adjust.notePlaceholder')}
          />
          <button className="primary" disabled={busy || !teamId} onClick={() => void submitAdjustment()}>
            {busy ? t('adjust.saving') : t('adjust.submit')}
          </button>
        </div>
        {message && <p className="muted">{message}</p>}
      </section>

      <section className="panel">
        <h3>{t('ledger.title')}</h3>
        <div className="table-card">
          <table>
            <thead><tr>
              <th>{t('ledger.when')}</th><th>{t('ledger.team')}</th><th>{t('ledger.amount')}</th>
              <th>{t('ledger.kind')}</th><th>{t('ledger.source')}</th><th>{t('ledger.externalId')}</th>
            </tr></thead>
            <tbody>
              {transactions.map(txRow => (
                <tr key={txRow.id}>
                  <td>{new Date(txRow.created_at).toLocaleString(dateLocale(locale))}</td>
                  <td>{txRow.team_name}</td>
                  <td className={Number(txRow.delta_usd) < 0 ? 'danger' : ''}>{fmtUsd(txRow.delta_usd)}</td>
                  <td>{txRow.kind}</td>
                  <td>{txRow.source}</td>
                  <td><code>{txRow.external_id || '—'}</code></td>
                </tr>
              ))}
            </tbody>
          </table>
          {!transactions.length && <EmptyState label={t('ledger.empty')} />}
        </div>
      </section>
    </div>
  );
}

function ResourceFrame({ t, children }: { t: (k: string) => string; children: React.ReactNode }) {
  return <div><h2 className="page-title">{t('billing.title')}</h2>{children}</div>;
}

function EmptyState({ label }: { label: string }) {
  return <p className="muted">{label}</p>;
}
