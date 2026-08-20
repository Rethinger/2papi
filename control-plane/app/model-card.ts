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
  if (strategy === 'p2c') return locale === 'ru' ? 'Выбор из двух (P2C)' : 'Power of two choices (P2C)';
  if (strategy === 'least_used' || strategy === 'least-used') return locale === 'ru' ? 'Наименее загруженный' : 'Least used';
  if (strategy === 'lkgp') return locale === 'ru' ? 'Последний успешный (LKGP)' : 'Last known good (LKGP)';
  if (strategy === 'reset_aware' || strategy === 'reset-aware') return locale === 'ru' ? 'С учётом сброса квоты' : 'Reset aware';
  if (strategy === 'adaptive') return locale === 'ru' ? 'Адаптивный' : 'Adaptive';
  return locale === 'ru' ? 'Ручной пул' : 'Manual pool';
}
