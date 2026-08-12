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
});
