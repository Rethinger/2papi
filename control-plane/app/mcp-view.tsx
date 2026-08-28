'use client';

// MCP servers management (OSS): table + create form over
// /api/control/v1/mcp-servers. Headers carry upstream credentials and are
// write-only here — the API never echoes them back.

import { useState } from 'react';

export interface McpServersViewProps {
  mcpServers: any[];
  locale: string;
  t: (key: string, vars?: Record<string, unknown>) => string;
  onDone: () => void;
}

const dateLocale = (locale: string) => (locale === 'ru' ? 'ru-RU' : 'en-US');

export function McpServersView({ mcpServers, locale, t, onDone }: McpServersViewProps) {
  const [name, setName] = useState('');
  const [url, setUrl] = useState('');
  const [headers, setHeaders] = useState('{"Authorization":"Bearer …"}');
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const rows = mcpServers ?? [];

  const call = async (method: string, path: string, body?: unknown) => {
    const res = await fetch(`/api/control/v1/${path}`, {
      method,
      headers: { 'Content-Type': 'application/json' },
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    });
    const json = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(json?.error?.message ?? `HTTP ${res.status}`);
    return json.data;
  };

  const parseHeaders = (): Record<string, string> | null => {
    const trimmed = headers.trim();
    if (!trimmed || trimmed === '{"Authorization":"Bearer …"}') return {};
    try {
      const parsed = JSON.parse(trimmed);
      if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) return parsed as Record<string, string>;
    } catch {}
    return null;
  };

  const createServer = async () => {
    const cleanHeaders = parseHeaders();
    if (cleanHeaders === null) {
      setError(t('mcp.headersInvalid' as never));
      return;
    }
    setBusy(true); setError(null); setMessage(null);
    try {
      await call('POST', 'mcp-servers', { name: name.trim(), url: url.trim(), enabled: true, headers: cleanHeaders });
      setMessage(t('mcp.created', { name: name.trim() }));
      setName(''); setUrl(''); setHeaders('{"Authorization":"Bearer …"}');
      onDone();
    } catch (cause: any) {
      setError(cause?.message ?? String(cause));
    } finally {
      setBusy(false);
    }
  };

  const toggleServer = async (row: any) => {
    setBusy(true); setError(null);
    try {
      await call('PATCH', `mcp-servers/${row.id}`, { enabled: !row.enabled });
      onDone();
    } catch (cause: any) {
      setError(cause?.message ?? String(cause));
    } finally {
      setBusy(false);
    }
  };

  const rotateHeaders = async (row: any) => {
    const raw = window.prompt(t('mcp.rotatePrompt', { name: row.name }), '{"Authorization":"Bearer new-token"}');
    if (raw === null) return;
    let clean: Record<string, string> | null = null;
    try {
      const parsed = JSON.parse(raw);
      if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
        clean = Object.fromEntries(Object.entries(parsed).filter(([, v]) => typeof v === 'string')) as Record<string, string>;
      }
    } catch {}
    if (!clean) { setError(t('mcp.headersInvalid')); return; }
    setBusy(true); setError(null);
    try {
      await call('PATCH', `mcp-servers/${row.id}`, { headers: clean });
      setMessage(t('mcp.rotated', { name: row.name }));
      onDone();
    } catch (cause: any) {
      setError(cause?.message ?? String(cause));
    } finally {
      setBusy(false);
    }
  };

  const deleteServer = async (row: any) => {
    if (!window.confirm(t('mcp.deleteConfirm', { name: row.name }))) return;
    setBusy(true); setError(null);
    try {
      await call('DELETE', `mcp-servers/${row.id}`);
      setMessage(t('mcp.deleted', { name: row.name }));
      onDone();
    } catch (cause: any) {
      setError(cause?.message ?? String(cause));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="page">
      <div className="page-heading compact">
        <div>
          <span className="eyebrow">{t('nav.mcp')}</span>
          <h1>{t('mcp.title')}</h1>
          <p>{t('mcp.description')}</p>
        </div>
      </div>

      <section className="panel" style={{ marginBottom: 16 }}>
        <div className="panel-heading"><h2>{t('mcp.addTitle')}</h2></div>
        <div className="form-row three">
          <input type="text" placeholder={t('mcp.namePlaceholder')} value={name} onChange={e => setName(e.target.value)} aria-label={t('mcp.namePlaceholder')} />
          <input type="text" placeholder={t('mcp.urlPlaceholder')} value={url} onChange={e => setUrl(e.target.value)} aria-label={t('mcp.urlPlaceholder')} />
          <input type="text" placeholder='{"Authorization":"Bearer …"}' value={headers} onChange={e => setHeaders(e.target.value)} aria-label={t('mcp.headersLabel')} />
        </div>
        <div style={{ marginTop: 11, display: 'flex', gap: 11, alignItems: 'center' }}>
          <button className="primary" disabled={busy || !name || !url} onClick={() => void createServer()}>
            {busy ? t('mcp.saving') : t('mcp.add')}
          </button>
          {error && <span style={{ color: '#ff9c9c' }}>{error}</span>}
          {message && <span style={{ color: 'var(--muted)' }}>{message}</span>}
        </div>
      </section>

      <section className="panel">
        <div className="panel-heading"><h2>{t('mcp.listTitle')}</h2></div>
        <div className="table-card">
          <table>
            <thead><tr>
              <th>{t('mcp.column.name')}</th>
              <th>{t('mcp.column.url')}</th>
              <th>{t('mcp.column.headers')}</th>
              <th>{t('mcp.column.status')}</th>
              <th />
            </tr></thead>
            <tbody>
              {rows.map(row => (
                <tr key={row.id}>
                  <td><b>{row.name}</b></td>
                  <td><code>{row.url}</code></td>
                  <td>{row.headers_set ? t('mcp.headersSet') : '—'}</td>
                  <td>{row.enabled ? t('mcp.enabled') : t('mcp.disabled')}</td>
                  <td>
                    <div className="row-actions">
                      <button className="ghost" disabled={busy} onClick={() => void toggleServer(row)}>
                        {row.enabled ? t('mcp.disable') : t('mcp.enable')}
                      </button>
                      <button className="ghost" disabled={busy} onClick={() => void rotateHeaders(row)}>{t('mcp.rotate')}</button>
                      <button className="ghost" disabled={busy} onClick={() => void deleteServer(row)}>{t('mcp.delete')}</button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {!rows.length && <p style={{ color: 'var(--muted)' }}>{t('mcp.empty')}</p>}
        </div>
      </section>
    </section>
  );
}
