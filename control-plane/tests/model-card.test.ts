import test from 'node:test';
import assert from 'node:assert/strict';
import { capabilityValue, formatContextWindow, strategyLabel } from '../app/model-card.ts';

test('formats model card context and explicit capability states', () => {
  assert.equal(formatContextWindow(272000, 'en', 'Unknown'), '272K');
  assert.equal(formatContextWindow(null, 'ru', 'Нет данных'), 'Нет данных');
  const labels = { yes: 'Yes', no: 'No', unknown: 'Unknown' };
  assert.equal(capabilityValue(false, labels), 'No');
  assert.equal(capabilityValue(null, labels), 'Unknown');
});

test('labels provider strategies exactly in both locales', () => {
  assert.equal(strategyLabel('round_robin', 'ru'), 'По очереди');
  assert.equal(strategyLabel('quota_failover', 'en'), 'Until quota is exhausted');
  assert.equal(strategyLabel('p2c', 'en'), 'Power of two choices (P2C)');
  assert.equal(strategyLabel('p2c', 'ru'), 'Выбор из двух (P2C)');
  assert.equal(strategyLabel('least_used', 'en'), 'Least used');
  assert.equal(strategyLabel('least-used', 'ru'), 'Наименее загруженный');
  assert.equal(strategyLabel('lkgp', 'en'), 'Last known good (LKGP)');
  assert.equal(strategyLabel('lkgp', 'ru'), 'Последний успешный (LKGP)');
  assert.equal(strategyLabel('reset_aware', 'en'), 'Reset aware');
  assert.equal(strategyLabel('reset-aware', 'ru'), 'С учётом сброса квоты');
});
