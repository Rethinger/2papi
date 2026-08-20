'use client';

import { GitBranchIcon, KeyIcon, OrganizationIcon, SearchIcon, ServerIcon } from '@primer/octicons-react';
import { useEffect, useMemo, useRef, useState } from 'react';
import type { View } from '../view-router';
import type { Translator } from '../i18n';

type PaletteEntry = {
  view: View;
  label: string;
  hint: string;
  icon: typeof ServerIcon;
};

export function CommandPalette({ data, t, onNavigate, onClose }: {
  data: { models: any[]; accounts: any[]; keys: any[]; teams: any[] };
  t: Translator;
  onNavigate: (view: View) => void;
  onClose: () => void;
}) {
  const [query, setQuery] = useState('');
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    inputRef.current?.focus();
    const closeOnEscape = (event: KeyboardEvent) => { if (event.key === 'Escape') onClose(); };
    window.addEventListener('keydown', closeOnEscape);
    return () => window.removeEventListener('keydown', closeOnEscape);
  }, [onClose]);

  const entries = useMemo<PaletteEntry[]>(() => {
    const q = query.trim().toLowerCase();
    if (!q) return [];
    const out: PaletteEntry[] = [];
    for (const model of data.models ?? []) {
      if (`${model.alias} ${model.upstream_model}`.toLowerCase().includes(q)) {
        out.push({ view: 'models', label: model.alias, hint: model.upstream_model, icon: GitBranchIcon });
      }
    }
    for (const account of data.accounts ?? []) {
      if (`${account.display_name} ${account.name}`.toLowerCase().includes(q)) {
        out.push({ view: 'accounts', label: account.display_name, hint: account.name, icon: ServerIcon });
      }
    }
    for (const key of data.keys ?? []) {
      if (`${key.name} ${key.key_prefix}`.toLowerCase().includes(q)) {
        out.push({ view: 'keys', label: key.name, hint: `${key.key_prefix}…`, icon: KeyIcon });
      }
    }
    for (const team of data.teams ?? []) {
      if (team.name.toLowerCase().includes(q)) {
        out.push({ view: 'teams', label: team.name, hint: t('teams.column.budget'), icon: OrganizationIcon });
      }
    }
    return out.slice(0, 24);
  }, [query, data, t]);

  const go = (entry: PaletteEntry) => {
    onNavigate(entry.view);
    onClose();
  };

  return (
    <div className="palette-backdrop" role="presentation" onMouseDown={event => { if (event.target === event.currentTarget) onClose(); }}>
      <div className="palette" role="dialog" aria-modal="true" aria-label={t('palette.title')}>
        <div className="palette-input">
          <SearchIcon size={16} />
          <input ref={inputRef} value={query} onChange={event => setQuery(event.target.value)} placeholder={t('palette.placeholder')} onKeyDown={event => {
            if (event.key === 'Enter' && entries[0]) go(entries[0]);
          }} />
          <kbd>Esc</kbd>
        </div>
        <div className="palette-list">
          {entries.length === 0 && <div className="palette-empty">{t('palette.empty')}</div>}
          {entries.map(entry => {
            const Icon = entry.icon;
            return (
              <button key={`${entry.view}:${entry.label}`} className="palette-item" onClick={() => go(entry)}>
                <Icon size={14} />
                <span>{entry.label}</span>
                <small>{entry.hint}</small>
              </button>
            );
          })}
        </div>
      </div>
    </div>
  );
}
