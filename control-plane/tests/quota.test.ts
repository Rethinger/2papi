import test from 'node:test';
import assert from 'node:assert/strict';
import { summarizeQuota, familyFor, kindFor } from '../lib/quota.ts';

function quotaMockClient(overrides?: { usage?: any[]; accounts?: any[] }) {
  return {
    query: async (sql: string): Promise<{ rows: any[] }> => {
      if (sql.includes('FROM request_events e') && sql.includes('request_event_attempts')) {
        return { rows: overrides?.usage ?? [
          { account: 'claude-primary', requests: '2', tokens: '12000' },
          { account: 'opencode', requests: '5', tokens: '3000' },
        ] };
      }
      if (sql.includes('FROM accounts a') && sql.includes('JOIN providers')) {
        return { rows: overrides?.accounts ?? [
          { name: 'claude-primary', enabled: true, adapter: 'anthropic', provider_name: 'Claude' },
          { name: 'opencode', enabled: true, adapter: 'opencode', provider_name: 'OpenCode' },
          { name: 'banned', enabled: false, adapter: 'openai-codex', provider_name: 'Codex' },
        ] };
      }
      throw new Error(sql);
    },
  } as any;
}

test('summarizeQuota aggregates usage, derives family/kind, flags disabled', async () => {
  const client = quotaMockClient();
  const quota = await summarizeQuota(client);
  assert.equal(quota.providers.length, 3);
  const claude = quota.providers.find(p => p.account === 'claude-primary');
  assert.ok(claude);
  assert.equal(claude.family, 'claude');
  assert.equal(claude.kind, 'oauth');
  assert.equal(claude.used, 12000);
  assert.equal(claude.enabled, true);
  assert.equal(claude.status, 'active');

  const free = quota.providers.find(p => p.account === 'opencode');
  assert.ok(free);
  assert.equal(free.family, 'free');
  assert.equal(free.kind, 'free');
  assert.equal(free.limit, null);

  const disabled = quota.providers.find(p => p.account === 'banned');
  assert.ok(disabled);
  assert.equal(disabled.enabled, false);
  assert.equal(disabled.status, 'disabled');
  assert.equal(disabled.family, 'codex');

  assert.equal(quota.summary.active, 2);
  assert.equal(quota.summary.used, 15000);
});

test('familyFor/kindFor map adapters', () => {
  assert.equal(familyFor('anthropic'), 'claude');
  assert.equal(familyFor('openai-codex'), 'codex');
  assert.equal(familyFor('deepseek'), 'deepseek');
  assert.equal(familyFor('opencode'), 'free');
  assert.equal(familyFor('felo'), 'free');
  assert.equal(familyFor('cursor'), 'cursor');
  assert.equal(familyFor('gemini'), 'gemini');
  assert.equal(familyFor(null), 'unknown');
  assert.equal(kindFor('opencode'), 'free');
  assert.equal(kindFor('anthropic'), 'oauth');
  assert.equal(kindFor('openai-codex'), 'oauth');
  assert.equal(kindFor('copilot'), 'oauth');
  assert.equal(kindFor('openai-compatible'), 'api_key');
});

test('summarizeQuota handles empty events', async () => {
  const client = quotaMockClient({ usage: [] });
  const quota = await summarizeQuota(client);
  assert.equal(quota.summary.used, 0);
  assert.equal(quota.providers.length, 3);
  assert.equal(quota.providers[1].status, 'unknown'); // enabled but no usage
});
