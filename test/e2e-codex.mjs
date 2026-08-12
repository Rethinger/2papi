#!/usr/bin/env node
import { strict as assert } from 'node:assert';
import { execFileSync } from 'node:child_process';
import crypto from 'node:crypto';

const base = 'http://127.0.0.1:13000/api/control/v1/';
const gatewayBase = 'http://127.0.0.1:18080';
const state = { accountId: null, providerId: null, accountWasEnabled: false, modelId: null, keyId: null, operation: null };

async function api(path, options = {}) {
  const response = await fetch(base + path, { ...options, headers: { 'Content-Type': 'application/json', ...(options.headers ?? {}) } });
  const payload = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(`${path} (${response.status}): ${JSON.stringify(payload)}`);
  return Object.prototype.hasOwnProperty.call(payload, 'data') ? payload.data : payload;
}

async function gateway(path, body) {
  const response = await fetch(gatewayBase + path, {
    method: 'POST', headers: { Authorization: `Bearer ${state.plaintextKey}`, 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  });
  const text = await response.text();
  assert.equal(response.status, 200, `${path}: ${response.status} ${text}`);
  return { response, text };
}

const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));

async function waitForAdoption(version) {
  const deadline = Date.now() + 30_000;
  while (Date.now() < deadline) {
    const ack = (await api('gateway-acks')).find(row => Number(row.version) === Number(version));
    if (ack) {
      assert.equal(ack.status, 'adopted', JSON.stringify(ack));
      return;
    }
    await sleep(500);
  }
  throw new Error(`gateway did not adopt version ${version}`);
}

function fakeCounters() {
  const raw = execFileSync('docker', ['compose', 'exec', '-T', 'fake-upstream', 'curl', '-fsS', 'http://127.0.0.1:9010/__test/counters'], { encoding: 'utf8' });
  return JSON.parse(raw);
}

async function connectFakeCodex() {
  const existing = (await api('accounts')).find(account => account.external_account_id === 'fake-account');
  state.accountWasEnabled = Boolean(existing?.enabled);
  const started = await api('codex/device/start', { method: 'POST', body: '{}' });
  assert.equal(started.user_code, 'FAKE-CODE');
  let status;
  for (let attempt = 0; attempt < 10; attempt++) {
    status = await api(`codex/device/${encodeURIComponent(started.session)}/status`);
    if (status.state === 'complete') break;
    await sleep(250);
  }
  assert.equal(status?.state, 'complete', JSON.stringify(status));
  state.accountId = status.account_id;
  const account = (await api('accounts')).find(item => item.id === state.accountId);
  state.providerId = account.provider_id;
  assert.equal(account.metadata.auth_method, 'device');
  assert.equal(account.plan_type, 'plus');
}

async function cleanup() {
  const errors = [];
  const attempt = async (label, work) => { try { await work(); } catch (error) { errors.push(`${label}: ${error.message}`); } };
  if (state.operation?.status === 'unknown') {
    await attempt('resolve operation', () => api(`accounts/${state.accountId}/quota/reset/${state.operation.id}/resolve`, { method: 'POST', body: JSON.stringify({ resolution: 'failed', note: 'Automated E2E cleanup after an inconclusive fake operation' }) }));
  }
  if (state.keyId) await attempt('disable key', () => api(`virtual-keys/${state.keyId}`, { method: 'DELETE' }));
  if (state.modelId) await attempt('disable model', () => api(`models/${state.modelId}`, { method: 'DELETE' }));
  if (state.accountId && !state.accountWasEnabled) await attempt('disable account', () => api(`accounts/${state.accountId}`, { method: 'DELETE' }));
  await attempt('publish cleanup', () => api('config-versions/publish', { method: 'POST', body: '{}' }));
  if (errors.length) throw new Error(`cleanup failed: ${errors.join('; ')}`);
}

async function run() {
  console.log('[Codex E2E] device auth → discovery → publish → inference → quota reset');
  await connectFakeCodex();

  const discovery = await api('model-discovery', { method: 'POST', body: JSON.stringify({ scope: 'account_id', account_id: state.accountId }) });
  assert.equal(discovery.results[0].status, 'succeeded', JSON.stringify(discovery));
  const discovered = (await api('discovered-models')).find(model => model.provider_id === state.providerId && model.upstream_model === 'gpt-5-codex');
  assert.ok(discovered?.accounts[state.accountId]?.available);

  const alias = `e2e-codex-${Date.now()}`;
  const model = await api('models/import-selection', { method: 'POST', body: JSON.stringify({ alias, provider_id: state.providerId, upstream_model: 'gpt-5-codex', routing_strategy: 'round_robin' }) });
  state.modelId = model.id;
  const key = await api('virtual-keys', { method: 'POST', body: JSON.stringify({ name: `e2e-codex-key-${Date.now()}`, models: [alias], rpm: 100, enabled: true }) });
  state.keyId = key.id;
  state.plaintextKey = key.plaintext_key;

  const published = await api('config-versions/publish', { method: 'POST', body: '{}' });
  await waitForAdoption(published.version);
  assert.ok((await api('models')).some(item => item.alias === alias));

  const native = await gateway('/v1/responses', { model: alias, input: 'hello', stream: false });
  const nativeJSON = JSON.parse(native.text);
  assert.equal(nativeJSON.model, alias);
  assert.equal(nativeJSON.output[0].content[0].text, 'fake codex reply');

  const chat = await gateway('/v1/chat/completions', { model: alias, messages: [{ role: 'user', content: 'hello' }], stream: false });
  const chatJSON = JSON.parse(chat.text);
  assert.equal(chatJSON.object, 'chat.completion');
  assert.equal(chatJSON.model, alias);
  assert.equal(chatJSON.choices[0].message.content, 'fake codex reply');

  const stream = await gateway('/v1/chat/completions', { model: alias, messages: [{ role: 'user', content: 'hello' }], stream: true });
  assert.match(stream.text, /"role":"assistant"/);
  assert.match(stream.text, /"content":"fake codex reply"/);
  assert.match(stream.text, /data: \[DONE\]/);

  const quota = await api(`accounts/${state.accountId}/quota/refresh`, { method: 'POST', body: '{}' });
  assert.equal(quota.quota.plan_type, 'plus');
  assert.equal(quota.reset_credits.available_count, 1);

  const idempotencyKey = crypto.randomUUID();
  state.operation = await api(`accounts/${state.accountId}/quota/reset`, { method: 'POST', headers: { 'Idempotency-Key': idempotencyKey }, body: JSON.stringify({ confirmed: true }) });
  assert.equal(state.operation.status, 'succeeded');
  const retried = await api(`accounts/${state.accountId}/quota/reset`, { method: 'POST', headers: { 'Idempotency-Key': idempotencyKey }, body: JSON.stringify({ confirmed: true }) });
  assert.equal(retried.id, state.operation.id);
  assert.equal(retried.status, 'succeeded');
  const counters = fakeCounters();
  assert.equal(counters.reset_consume, 1);
  assert.equal(counters.reset_consume_calls, 1);
  assert.ok(counters.inference >= 3);
  console.log('[Codex E2E] ✓ Responses, Chat, SSE, quota and idempotent reset passed');
}

let failure;
try { await run(); } catch (error) { failure = error; }
try { await cleanup(); } catch (error) { failure = failure ? new AggregateError([failure, error], 'E2E and cleanup failed') : error; }
if (failure) throw failure;
