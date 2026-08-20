import assert from 'node:assert/strict';
import test from 'node:test';
import { clampQuotaPercent, codexPlanLabel, isValidPublicAlias, MAX_AUTH_FILE_BYTES, splitRemaining } from '../app/codex-client.ts';
import { messages, translate } from '../app/i18n.ts';

test('Codex UI exposes every authentication and device state in both locales', () => {
  for (const locale of ['en', 'ru'] as const) {
    for (const key of ['codex.tab.browser', 'codex.tab.device', 'codex.tab.import'] as const) {
      assert.ok(translate(locale, key).length > 3);
    }
    for (const state of ['pending', 'slow_down', 'expired', 'denied', 'failed', 'complete'] as const) {
      const key = `codex.device.state.${state}` as keyof typeof messages.en;
      assert.notEqual(messages[locale][key], state);
    }
  }
});

test('public alias validation matches the control-plane contract', () => {
  for (const alias of ['gpt-5-codex', 'codex/fast', 'team:model.v2']) assert.equal(isValidPublicAlias(alias), true);
  for (const alias of ['', 'has space', '-leading', 'trailing/', 'line\nbreak']) assert.equal(isValidPublicAlias(alias), false);
});

test('plan labels preserve unknown upstream plans and auth import is capped at one MiB', () => {
  assert.equal(codexPlanLabel('plus', 'Unknown'), 'Plus');
  assert.equal(codexPlanLabel('regional-preview', 'Unknown'), 'regional-preview');
  assert.equal(codexPlanLabel(null, 'Unknown'), 'Unknown');
  assert.equal(MAX_AUTH_FILE_BYTES, 1024 * 1024);
});

test('countdown splits remaining time and never goes negative', () => {
  assert.deepEqual(splitRemaining(2 * 3600 * 1000 + 13 * 60 * 1000 + 5 * 1000), { hours: 2, minutes: 13, seconds: 5 });
  assert.deepEqual(splitRemaining(45 * 1000), { hours: 0, minutes: 0, seconds: 45 });
  assert.deepEqual(splitRemaining(-10_000), { hours: 0, minutes: 0, seconds: 0 });
});

test('quota UI clamps upstream percentages and localizes refresh state', () => {
  assert.equal(clampQuotaPercent(-2), 0);
  assert.equal(clampQuotaPercent(42.5), 42.5);
  assert.equal(clampQuotaPercent(140), 100);
  assert.equal(clampQuotaPercent('bad'), 0);
  for (const locale of ['en', 'ru'] as const) {
    for (const key of ['codex.quota.refresh', 'codex.quota.primary', 'codex.quota.resets', 'codex.quota.resetsIn', 'codex.quota.updated', 'codex.quota.resetCredits'] as const) {
      assert.ok(translate(locale, key).length > 3);
    }
  }
});

test('quota reset risk and resolution states are localized without raw statuses', () => {
  const keys = [
    'codex.quota.reset', 'codex.quota.resetConfirmTitle', 'codex.quota.resetConfirmBody',
    'codex.quota.resetConfirmAction', 'codex.quota.resetUnknown', 'codex.quota.reconcile',
    'codex.quota.resolveWarning', 'codex.quota.resolveNote', 'codex.quota.resolveSucceeded',
    'codex.quota.resolveFailed', 'codex.quota.creditExpires',
  ] as const;
  for (const locale of ['en', 'ru'] as const) {
    for (const key of keys) {
      const rendered = translate(locale, key);
      assert.ok(rendered.length > 4, `${locale}:${key}`);
      assert.notEqual(rendered, 'unknown');
      assert.notEqual(rendered, 'succeeded');
      assert.notEqual(rendered, 'failed');
    }
  }
});

test('resource management actions and provider section have RU and EN labels', () => {
  const keys = [
    'action.edit', 'action.delete', 'accounts.providersTitle', 'accounts.providersDescription',
    'modal.provider.editTitle', 'modal.account.editTitle', 'modal.model.editTitle', 'modal.key.editTitle',
    'delete.providerTitle', 'delete.providerBody', 'delete.accountTitle', 'delete.accountBody', 'delete.confirm',
  ] as const;
  for (const locale of ['en', 'ru'] as const) {
    for (const key of keys) assert.ok(translate(locale, key).length > 3, `${locale}:${key}`);
  }
});

test('request trace metrics and outcomes are localized in both interfaces', () => {
  const keys = [
    'nav.requests',
    'overview.metric.requests24h',
    'overview.metric.successRate',
    'overview.metric.p95Latency',
    'overview.metric.tokens24h',
    'requests.title',
    'requests.description',
    'requests.attempts',
    'requests.outcome.success',
    'requests.outcome.rateLimited',
    'requests.outcome.upstreamError',
    'requests.outcome.saturated',
    'requests.outcome.canceled',
    'requests.outcome.rejected',
  ] as const;
  for (const locale of ['en', 'ru'] as const) {
    for (const key of keys) {
      const rendered = translate(locale, key);
      assert.ok(rendered.length > 2, `${locale}:${key}`);
    }
  }
});
