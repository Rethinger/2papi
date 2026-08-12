#!/usr/bin/env node
import { strict as assert } from 'node:assert';

const base = 'http://127.0.0.1:13000/api/control/v1/';
const gwBase = 'http://127.0.0.1:18080';

async function api(path, opts = {}) {
  const r = await fetch(base + path, { ...opts, headers: { 'Content-Type': 'application/json', ...opts.headers } });
  const j = await r.json();
  if (!r.ok) throw new Error(`${path}: ${JSON.stringify(j)}`);
  return j.data;
}

async function gateway(path, opts = {}) {
  const r = await fetch(gwBase + path, opts);
  if (!r.ok) throw new Error(`gateway ${path}: ${r.status}`);
  return r;
}

async function sleep(ms) { await new Promise(resolve => setTimeout(resolve, ms)); }

async function waitForAdoption(version, timeoutMs = 30000) {
  const deadline = Date.now() + timeoutMs;
  let seen = [];
  while (Date.now() < deadline) {
    const acks = await api('gateway-acks');
    const match = acks.find(a => Number(a.version) === Number(version));
    if (match) {
      assert.equal(match.status, 'adopted', `gateway rejected version ${version}: ${match.error ?? 'unknown error'}`);
      return match;
    }
    seen = acks.slice(0, 3).map(a => `${a.version}:${a.status}`);
    await sleep(1000);
  }
  throw new Error(`gateway did not acknowledge version ${version} within ${timeoutMs}ms (latest acks: ${seen.join(', ') || 'none'})`);
}

// Tracked so cleanup can run even when an assertion fails midway.
const state = { accountId: null, accountWasPreexisting: false, modelId: null, baselineAccountIds: null, keyId: null };

async function cleanup() {
  const drop = async (label, fn) => {
    try { await fn(); } catch (e) { console.error(`[E2E] cleanup ${label} failed: ${e.message}`); }
  };
  if (state.keyId) await drop('virtual key', () => api(`virtual-keys/${state.keyId}`, { method: 'DELETE' }));
  if (state.modelId && state.baselineAccountIds) {
    await drop('model routing', () => api(`models/${state.modelId}`, { method: 'PATCH', body: JSON.stringify({ accounts: state.baselineAccountIds }) }));
  }
  if (state.accountId && !state.accountWasPreexisting) {
    await drop('account', () => api(`accounts/${state.accountId}`, { method: 'DELETE' }));
  }
  // Leave the control plane on a published snapshot that matches the restored state.
  await drop('final publish', () => api('config-versions/publish', { method: 'POST', body: '{}' }));
  console.log('[E2E] Cleanup complete');
}

async function run() {
  console.log('[E2E] Starting create → publish → adopt → request → rollback → verify');

  // 1. Baseline snapshot
  const baseline = await api('config-versions');
  const baselineVersion = baseline[0]?.version ?? 0;
  console.log(`[E2E] Baseline version: ${baselineVersion}`);

  // 2. Create the test account
  const accounts = await api('accounts');
  const existing = accounts.find(a => a.name === 'e2e-test');
  if (existing) {
    state.accountId = existing.id;
    state.accountWasPreexisting = true;
    console.log('[E2E] Reusing existing e2e-test account');
  } else {
    const providers = await api('providers');
    const created = await api('accounts', {
      method: 'POST',
      body: JSON.stringify({
        provider_id: providers[0].id,
        name: 'e2e-test',
        display_name: 'E2E Test Account',
        base_url: 'http://fake-upstream:9003',
        enabled: true,
        priority: 3,
        weight: 1,
        max_concurrency: 50,
        cost: 0.1,
        credential: { api_key: 'e2e-test-key' },
        metadata: {},
      }),
    });
    state.accountId = created.id;
    console.log(`[E2E] Created account: ${state.accountId}`);
  }

  // 3. Attach the account to an existing model alias
  const models = await api('models');
  const testModel = models.find(model => model.enabled && model.alias === 'gpt-dev')
    ?? models.find(model => model.enabled);
  assert.ok(testModel, 'no enabled model is available for the generic E2E route');
  state.modelId = testModel.id;
  state.baselineAccountIds = testModel.accounts.slice();
  if (!state.baselineAccountIds.includes(state.accountId)) {
    await api(`models/${testModel.id}`, {
      method: 'PATCH',
      body: JSON.stringify({ accounts: [...state.baselineAccountIds, state.accountId] }),
    });
    const reread = (await api('models')).find(m => m.id === testModel.id);
    assert.ok(reread.accounts.includes(state.accountId), 'PATCH /models did not persist the new account');
    console.log(`[E2E] Added e2e-test to model ${testModel.alias}`);
  }

  // 4. Publish and wait for the gateway to adopt that exact version
  const published = await api('config-versions/publish', { method: 'POST', body: '{}' });
  console.log(`[E2E] Published version ${published.version}, checksum ${published.checksum.slice(0, 10)}`);
  await waitForAdoption(published.version);
  console.log(`[E2E] Gateway adopted version ${published.version}`);

  // 5. Mint a virtual key so authentication is genuinely exercised
  const createdKey = await api('virtual-keys', {
    method: 'POST',
    body: JSON.stringify({ name: `e2e-key-${Date.now()}`, enabled: true, models: [testModel.alias], rpm: 100 }),
  });
  state.keyId = createdKey.id;
  const plaintext = createdKey.plaintext_key ?? createdKey.key;
  assert.ok(plaintext && plaintext.startsWith('sk-'), `no plaintext key returned: ${JSON.stringify(createdKey)}`);
  console.log(`[E2E] Created virtual key ${createdKey.key_prefix ?? plaintext.slice(0, 10)}`);

  const keyPublish = await api('config-versions/publish', { method: 'POST', body: '{}' });
  console.log(`[E2E] Published version ${keyPublish.version} carrying the new key`);
  await waitForAdoption(keyPublish.version);

  const streamRes = await gateway('/v1/chat/completions', {
    method: 'POST',
    headers: { Authorization: `Bearer ${plaintext}`, 'Content-Type': 'application/json' },
    body: JSON.stringify({ model: testModel.alias, stream: true, messages: [{ role: 'user', content: 'e2e' }] }),
  });
  assert.equal(streamRes.status, 200, 'streaming request with new key failed');
  const route = streamRes.headers.get('x-gateway-route');
  await streamRes.text();
  console.log(`[E2E] Streaming request authorised with new key, routed to: ${route}`);

  // Negative check: an unknown key must be rejected
  const bogus = await fetch(`${gwBase}/v1/chat/completions`, {
    method: 'POST',
    headers: { Authorization: 'Bearer sk-definitely-not-valid', 'Content-Type': 'application/json' },
    body: JSON.stringify({ model: testModel.alias, messages: [{ role: 'user', content: 'e2e' }] }),
  });
  assert.equal(bogus.status, 401, `bogus key should be 401, got ${bogus.status}`);
  console.log('[E2E] Bogus key correctly rejected with 401');

  // 6. Rollback to the baseline version and verify it is restored
  if (baselineVersion > 0) {
    await api('config-versions/rollback', { method: 'POST', body: JSON.stringify({ version: baselineVersion }) });
    const rolledBack = await api('config-versions/publish', { method: 'POST', body: '{}' });
    console.log(`[E2E] Rolled back and published version ${rolledBack.version}`);
    await waitForAdoption(rolledBack.version);

    const latest = (await api('config-versions'))[0];
    assert.ok(
      latest.source_version === baselineVersion || latest.version === baselineVersion,
      `rollback did not restore baseline ${baselineVersion}, got version ${latest.version} source ${latest.source_version}`,
    );
    console.log(`[E2E] Rollback verified: latest version ${latest.version}, source ${latest.source_version ?? 'n/a'}`);
  }
}

try {
  await run();
  console.log('[E2E] ✓ All phases passed: create → publish → adopt → request → rollback → verify');
} finally {
  await cleanup();
}
