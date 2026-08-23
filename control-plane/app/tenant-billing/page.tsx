'use client';

// Tenant billing page (Cloud): balance, keys and credit ledger for the
// signed-in user's own team. Operator view lives in the dashboard; this
// page is self-serve and session-scoped.

import { useEffect, useState } from 'react';

interface BillingData {
  team: { id: string; name: string } | null;
  balance_usd: number;
  transactions: Array<{ delta_usd: number; kind: string; source: string; external_id: string; note: string; created_at: string }>;
  keys: Array<{ name: string; key_prefix: string; enabled: boolean }>;
  checkout_url?: string;
}

function fmtUsd(v: unknown): string {
  const n = Number(v ?? 0);
  return `${n < 0 ? '-' : ''}$${Math.abs(n).toFixed(4)}`;
}

export default function TenantBillingPage() {
  const [data, setData] = useState<BillingData | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [copied, setCopied] = useState<string | null>(null);

  useEffect(() => {
    fetch('/api/auth/billing', { headers: { 'Content-Type': 'application/json' } })
      .then(async res => {
        if (res.status === 401) { window.location.href = '/'; return null as any; }
        if (!res.ok) throw new Error('failed');
        return res.json();
      })
      .then(json => { if (json) setData(json.data); })
      .catch(() => setError('Could not load billing data.'));
  }, []);

  const copyKey = async (name: string, prefix: string) => {
    // Plaintext keys are shown once at creation; here we can only copy the
    // prefix as a hint. Full rotation comes with the keys API for tenants.
    try { await navigator.clipboard.writeText(prefix); setCopied(name); setTimeout(() => setCopied(null), 1500); } catch {}
  };

  if (error) {
    return <main style={{ maxWidth: 680, margin: '60px auto', fontFamily: 'system-ui' }}><h1>Billing</h1><p>{error}</p><p><a href="/">Back to dashboard</a></p></main>;
  }
  if (!data) {
    return <main style={{ maxWidth: 680, margin: '60px auto', fontFamily: 'system-ui' }}><h1>Billing</h1><p>Loading…</p></main>;
  }

  const lowBalance = Number(data.balance_usd) <= 0;

  return (
    <main style={{ maxWidth: 760, margin: '40px auto', padding: '0 16px', fontFamily: 'system-ui' }}>
      <h1>Billing — {data.team?.name ?? 'your team'}</h1>

      <section style={{ border: '1px solid #e2dccf', borderRadius: 14, padding: 18, marginBottom: 18 }}>
        <div style={{ fontSize: 13, color: '#8a8375' }}>Prepaid balance</div>
        <div style={{ fontSize: 34, fontWeight: 700, color: lowBalance ? '#b3474c' : undefined }}>{fmtUsd(data.balance_usd)}</div>
        {lowBalance && <p style={{ color: '#b3474c', margin: 0 }}>Top up to keep your keys working.</p>}
        {data.checkout_url
          ? <a href={data.checkout_url} target="_blank" rel="noreferrer"><button style={{ marginTop: 10, padding: '8px 14px', cursor: 'pointer' }}>Add credits ↗</button></a>
          : <p style={{ color: '#8a8375', fontSize: 13 }}>Checkout link appears once payments are configured.</p>}
      </section>

      <section style={{ border: '1px solid #e2dccf', borderRadius: 14, padding: 18, marginBottom: 18 }}>
        <h2 style={{ marginTop: 0 }}>Keys</h2>
        <table width="100%" cellPadding={6}>
          <thead><tr style={{ textAlign: 'left', color: '#8a8375' }}><th>Name</th><th>Prefix</th><th>Status</th><th /></tr></thead>
          <tbody>
            {data.keys.map(k => (
              <tr key={k.name}>
                <td>{k.name}</td>
                <td><code>{k.key_prefix}…</code> <button onClick={() => void copyKey(k.name, k.key_prefix)} style={{ cursor: 'pointer' }}>{copied === k.name ? '✓' : 'copy prefix'}</button></td>
                <td>{k.enabled ? 'active' : 'disabled'}</td>
                <td />
              </tr>
            ))}
          </tbody>
        </table>
        {!data.keys.length && <p style={{ color: '#8a8375' }}>No keys yet.</p>}
      </section>

      <section style={{ border: '1px solid #e2dccf', borderRadius: 14, padding: 18 }}>
        <h2 style={{ marginTop: 0 }}>Credit history</h2>
        <table width="100%" cellPadding={6}>
          <thead><tr style={{ textAlign: 'left', color: '#8a8375' }}><th>When</th><th>Amount</th><th>Kind</th><th>Source</th></tr></thead>
          <tbody>
            {data.transactions.map((txRow, i) => (
              <tr key={i}>
                <td>{new Date(txRow.created_at).toLocaleString()}</td>
                <td style={Number(txRow.delta_usd) < 0 ? { color: '#b3474c' } : { color: '#2e7d32' }}>{fmtUsd(txRow.delta_usd)}</td>
                <td>{txRow.kind}</td>
                <td>{txRow.source}</td>
              </tr>
            ))}
          </tbody>
        </table>
        {!data.transactions.length && <p style={{ color: '#8a8375' }}>No transactions yet.</p>}
      </section>

      <p style={{ marginTop: 22 }}><a href="/">← Back to dashboard</a></p>
    </main>
  );
}
