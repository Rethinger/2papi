import assert from 'node:assert/strict';
import test from 'node:test';
import {
  createTranslator,
  dateLocale,
  isLocale,
  localeFromAcceptLanguage,
  messages,
  resolveLocale,
  translate,
} from '../app/i18n.ts';

test('locale guard accepts only supported locales', () => {
  assert.equal(isLocale('en'), true);
  assert.equal(isLocale('ru'), true);
  assert.equal(isLocale('de'), false);
  assert.equal(isLocale(null), false);
});

test('Accept-Language chooses the highest-priority supported locale', () => {
  assert.equal(localeFromAcceptLanguage('ru-RU,ru;q=0.9,en;q=0.8'), 'ru');
  assert.equal(localeFromAcceptLanguage('de-DE,ru;q=0.8,en;q=0.7'), 'ru');
  assert.equal(localeFromAcceptLanguage('ru;q=0.2,en-US;q=0.9'), 'en');
  assert.equal(localeFromAcceptLanguage('ru;q=0,en;q=0.5'), 'en');
  assert.equal(localeFromAcceptLanguage('de-DE'), 'en');
  assert.equal(localeFromAcceptLanguage(null), 'en');
});

test('persisted locale overrides browser preference and invalid values are ignored', () => {
  assert.equal(resolveLocale('en', 'ru-RU'), 'en');
  assert.equal(resolveLocale('ru', 'en-US'), 'ru');
  assert.equal(resolveLocale('invalid', 'ru-RU'), 'ru');
  assert.equal(resolveLocale(undefined, undefined), 'en');
});

test('English and Russian dictionaries have identical keys', () => {
  assert.deepEqual(Object.keys(messages.ru).sort(), Object.keys(messages.en).sort());
  assert.ok(Object.keys(messages.en).length > 100);
});

test('translations interpolate named values and retain missing placeholders', () => {
  assert.equal(translate('en', 'overview.metric.configured', { count: 4 }), '4 configured');
  assert.equal(translate('ru', 'overview.metric.configured', { count: 4 }), 'Настроено: 4');
  assert.equal(translate('en', 'overview.metric.configured'), '{count} configured');
  assert.equal(createTranslator('ru')('modal.rotation.title', { name: 'Primary' }), 'Замена ключа · Primary');
});

test('date locale follows the active interface locale', () => {
  assert.equal(dateLocale('en'), 'en-US');
  assert.equal(dateLocale('ru'), 'ru-RU');
});
