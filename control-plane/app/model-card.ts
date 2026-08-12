import type { Locale } from './i18n';

export function formatContextWindow(value: unknown, locale: Locale, unknown: string) {
  if (typeof value !== 'number' || !Number.isFinite(value) || value <= 0) return unknown;
  return new Intl.NumberFormat(locale === 'ru' ? 'ru-RU' : 'en-US', { notation: value >= 1000 ? 'compact' : 'standard', maximumFractionDigits: 1 }).format(value);
}

export function capabilityValue(value: boolean | null | undefined, labels: { yes: string; no: string; unknown: string }) {
  return value === true ? labels.yes : value === false ? labels.no : labels.unknown;
}

export function strategyLabel(strategy: string, locale: Locale) {
  if (strategy === 'round_robin') return locale === 'ru' ? 'По очереди' : 'Round robin';
  if (strategy === 'quota_failover') return locale === 'ru' ? 'До исчерпания квоты' : 'Until quota is exhausted';
  return locale === 'ru' ? 'Ручной пул' : 'Manual pool';
}
