import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import { Pool } from 'pg';
import {
  accountUsageSince,
  RequestEventBatchSchema,
  listRequestEvents,
  requestMetrics,
  storeRequestEvents,
} from '../lib/request-events.ts';

const url = process.env.TEST_DATABASE_URL;
const options = { skip: url ? false : 'TEST_DATABASE_URL is not set' };
const pool = url ? new Pool({ connectionString: url, max: 2 }) : null;

test.before(async () => {
  if (!pool) return;
  await pool.query('DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;');
  const dir = path.join(process.cwd(), 'migrations');
  for (const name of (await fs.readdir(dir)).filter(name => name.endsWith('.sql')).sort()) {
    await pool.query(await fs.readFile(path.join(dir, name), 'utf8'));
  }
});

test.after(async () => { await pool?.end(); });

test('request event schema rejects payload capture fields', () => {
  const result = RequestEventBatchSchema.safeParse({
    gateway_id: 'gateway-a',
    events: [{
      request_id: '11111111111111111111111111111111', occurred_at: new Date().toISOString(), endpoint: '/v1/responses',
      public_model: 'public', upstream_model: 'upstream', virtual_key: 'vk', streaming: false,
      config_version: 7, final_status: 200, success: true, total_latency_ms: 12,
      input_tokens: 3, output_tokens: 4, total_tokens: 7, attempts: [], prompt: 'must not persist',
    }],
  });
  assert.equal(result.success, false);
});

test('request event schema enforces bounded batches and metric invariants', () => {
  const event = {
    request_id: 'a'.repeat(32), occurred_at: new Date().toISOString(), endpoint: '/v1/responses',
    public_model: 'public', upstream_model: 'upstream', virtual_key: 'vk', streaming: false,
    config_version: 7, final_status: 200, success: true, total_latency_ms: 12,
    input_tokens: 3, output_tokens: 4, total_tokens: 7, attempts: [],
  };
  assert.equal(RequestEventBatchSchema.safeParse({
    gateway_id: 'gateway-a',
    events: Array.from({ length: 11 }, (_, index) => ({ ...event, request_id: index.toString().padStart(32, '0') })),
  }).success, false);
  assert.equal(RequestEventBatchSchema.safeParse({
    gateway_id: 'gateway-a',
    events: [{ ...event, endpoint: '/v1/arbitrary' }],
  }).success, false);
  assert.equal(RequestEventBatchSchema.safeParse({
    gateway_id: 'gateway-a',
    events: [{ ...event, success: false }],
  }).success, false);
  assert.equal(RequestEventBatchSchema.safeParse({
    gateway_id: 'gateway-a',
    events: [{ ...event, total_tokens: 8 }],
  }).success, false);
});

test('request events persist idempotently with ordered attempts and metrics', options, async () => {
  const batch = RequestEventBatchSchema.parse({
    gateway_id: 'gateway-a',
    events: [
      {
        request_id: '22222222222222222222222222222222', occurred_at: '2026-08-13T10:00:00.000Z', endpoint: '/v1/responses',
        public_model: 'public', upstream_model: 'upstream', virtual_key: 'vk', streaming: false,
        config_version: 7, final_status: 200, success: true, total_latency_ms: 40,
        input_tokens: 3, output_tokens: 4, total_tokens: 7,
        attempts: [
          { account: 'primary', adapter: 'openai-compatible', status: 429, outcome: 'rate_limited', latency_ms: 5, cooldown_ms: 30000 },
          { account: 'secondary', adapter: 'openai-compatible', status: 200, outcome: 'success', latency_ms: 30 },
        ],
      },
      {
        request_id: '33333333333333333333333333333333', occurred_at: '2026-08-13T10:01:00.000Z', endpoint: '/v1/chat/completions',
        public_model: 'public', upstream_model: 'upstream', virtual_key: 'vk', streaming: true,
        config_version: 7, final_status: 502, success: false, total_latency_ms: 20,
        input_tokens: 0, output_tokens: 0, total_tokens: 0,
        attempts: [{ account: 'primary', adapter: 'openai-compatible', status: 500, outcome: 'upstream_error', latency_ms: 19 }],
      },
    ],
  });

  await storeRequestEvents(pool!, batch.gateway_id, batch.events);
  await storeRequestEvents(pool!, batch.gateway_id, batch.events);

  const events = await listRequestEvents(pool!, { limit: 20 });
  assert.equal(events.length, 2);
  const fallback = events.find(event => event.request_id === '22222222222222222222222222222222');
  assert.deepEqual(fallback?.attempts.map(attempt => attempt.account), ['primary', 'secondary']);
  assert.equal(fallback?.attempts[0].outcome, 'rate_limited');

  const metrics = await requestMetrics(pool!, { since: '2026-08-13T00:00:00.000Z' });
  assert.equal(metrics.requests, 2);
  assert.equal(metrics.successful, 1);
  assert.equal(metrics.fallbacks, 1);
  assert.equal(metrics.total_tokens, 7);
  assert.equal(metrics.p95_latency_ms, 39);

  await pool!.query(
    `UPDATE request_events SET received_at=now() - interval '31 days' WHERE request_id='33333333333333333333333333333333'`,
  );
  await storeRequestEvents(pool!, batch.gateway_id, [{
    ...batch.events[0],
    request_id: '44444444444444444444444444444444',
    occurred_at: new Date().toISOString(),
  }]);
  const retained = await listRequestEvents(pool!, { limit: 20 });
  assert.equal(retained.some(event => event.request_id === '33333333333333333333333333333333'), false);
});

test('account usage sums tokens since a cutoff per account', { skip: options.skip }, async () => {
  const provider = await pool!.query(
    `INSERT INTO providers (slug, name, adapter, base_url, enabled) VALUES ('usage-prov', 'Usage Provider', 'openai-codex', 'https://example.test', true) RETURNING id`,
  );
  const account = await pool!.query(
    `INSERT INTO accounts (provider_id, name, display_name, base_url, enabled, priority, weight, max_concurrency, cost)
     VALUES ($1, 'usage-account', 'Usage Account', 'https://example.test', true, 1, 1, 1, 0) RETURNING id`,
    [provider.rows[0].id],
  );
  const baseEvent = {
    occurred_at: new Date().toISOString(), endpoint: '/v1/responses',
    public_model: 'usage-public', upstream_model: 'usage-upstream', virtual_key: 'vk', streaming: false,
    config_version: 1, final_status: 200, success: true, total_latency_ms: 5,
    input_tokens: 10, output_tokens: 20, total_tokens: 30,
  };
  await pool!.query(`INSERT INTO request_events (gateway_id, request_id, occurred_at, endpoint, public_model, total_latency_ms, final_status, success, input_tokens, output_tokens, total_tokens)
    VALUES ('gw', '55555555555555555555555555555555', now(), '/v1/responses', 'usage-public', 5, 200, true, 10, 20, 30)`);
  await pool!.query(`INSERT INTO request_event_attempts (request_event_id, position, account, adapter, status, outcome, latency_ms)
    SELECT id, 0, 'usage-account', 'openai-codex', 200, 'success', 5 FROM request_events WHERE request_id='55555555555555555555555555555555'`);
  await pool!.query(`INSERT INTO request_events (gateway_id, request_id, occurred_at, endpoint, public_model, total_latency_ms, final_status, success, input_tokens, output_tokens, total_tokens)
    VALUES ('gw', '66666666666666666666666666666666', now() - interval '3 hours', '/v1/responses', 'usage-public', 5, 200, true, 1, 2, 3)`);
  await pool!.query(`INSERT INTO request_event_attempts (request_event_id, position, account, adapter, status, outcome, latency_ms)
    SELECT id, 0, 'usage-account', 'openai-codex', 200, 'success', 5 FROM request_events WHERE request_id='66666666666666666666666666666666'`);
  await pool!.query(`INSERT INTO request_events (gateway_id, request_id, occurred_at, endpoint, public_model, total_latency_ms, final_status, success, input_tokens, output_tokens, total_tokens)
    VALUES ('gw', '77777777777777777777777777777777', now() - interval '3 hours', '/v1/responses', 'usage-public', 5, 200, true, 100, 200, 300)`);
  await pool!.query(`INSERT INTO request_event_attempts (request_event_id, position, account, adapter, status, outcome, latency_ms)
    SELECT id, 0, 'other-account', 'openai-codex', 200, 'success', 5 FROM request_events WHERE request_id='77777777777777777777777777777777'`);

  const recent = await accountUsageSince(pool!, 'usage-account', new Date(Date.now() - 60 * 60 * 1000));
  assert.deepEqual(recent, { tokens: 30, requests: 1 });
  const wide = await accountUsageSince(pool!, 'usage-account', new Date(Date.now() - 24 * 60 * 60 * 1000));
  assert.deepEqual(wide, { tokens: 33, requests: 2 });
  const other = await accountUsageSince(pool!, 'other-account', new Date(Date.now() - 24 * 60 * 60 * 1000));
  assert.deepEqual(other, { tokens: 300, requests: 1 });
  const none = await accountUsageSince(pool!, 'missing-account', new Date(Date.now() - 24 * 60 * 60 * 1000));
  assert.deepEqual(none, { tokens: 0, requests: 0 });
});
