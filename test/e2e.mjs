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

console.log('[E2E] Starting create → publish → adopt → request → rollback → verify');

// 1. Baseline snapshot
const baseline = await api('config-versions');
const baselineVersion = baseline[0]?.version ?? 0;
console.log(`[E2E] Baseline version: ${baselineVersion}`);

// 2. Create new account
const accounts = await api('accounts');
const testAccount = accounts.find(a => a.name === 'e2e-test');
let accountId;
if (testAccount) {
  accountId = testAccount.id;
  console.log('[E2E] Reusing existing e2e-test account');
} else {
  const providers = await api('providers');
  const provider = providers[0];
  const newAccount = await api('accounts', {
    method: 'POST',
    body: JSON.stringify({
      provider_id: provider.id,
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
  accountId = newAccount.id;
  console.log(`[E2E] Created account: ${accountId}`);
}

// 3. Add account to existing model
const models = await api('models');
const testModel = models[0];
const baselineAccountIds = testModel.accounts.slice();
if (!baselineAccountIds.includes(accountId)) {
  await api(`models/${testModel.id}`, {
    method: 'PATCH',
    body: JSON.stringify({ accounts: [...baselineAccountIds, accountId] }),
  });
  console.log(`[E2E] Added e2e-test to model ${testModel.alias}`);
}

// 4. Publish and wait for adoption
const published = await api('config-versions/publish', { method: 'POST', body: '{}' });
console.log(`[E2E] Published version ${published.version}, checksum ${published.checksum.slice(0, 10)}`);
await sleep(5000);

async function waitForAdoption(version, timeoutMs = 20000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const versions = await api('config-versions');
    const row = versions.find(v => v.version === version);
    if (row && (row.adopted_at || row.status === 'adopted')) return row;
    const events = await api('audit-events?resource_type=config_versions&action=adopted');
    if (events.some(e => String(e.payload?.version ?? e.resource_id) === String(version))) return row;
    await sleep(1000);
  }
  throw new Error(`gateway did not adopt version ${version} within ${timeoutMs}ms`);
}

await waitForAdoption(published.version);
console.log(`[E2E] Gateway adopted version ${published.version}`);

// 5. Create a dedicated virtual key so auth is genuinely exercised
const createdKey = await api('virtual-keys', {
  method: 'POST',
  body: JSON.stringify({ name: `e2e-key-${Date.now()}`, enabled: true, models: [testModel.alias], rpm: 100 }),
});
const plaintext = createdKey.plaintext_key ?? createdKey.key;
assert.ok(plaintext && plaintext.startsWith('sk-'), `no plaintext key returned: ${JSON.stringify(createdKey)}`);
console.log(`[E2E] Created virtual key ${createdKey.key_prefix ?? plaintext.slice(0, 10)}`);

// Key only reaches the gateway after a publish
const keyPublish = await api('config-versions/publish', { method: 'POST', body: '{}' });
console.log(`[E2E] Published version ${keyPublish.version} carrying the new key`);
await sleep(5000);

const streamRes = await gateway('/v1/chat/completions', {
  method: 'POST',
  headers: { Authorization: `Bearer ${plaintext}`, 'Content-Type': 'application/json' },
  body: JSON.stringify({ model: testModel.alias, stream: true, messages: [{ role: 'user', content: 'e2e' }] }),
});
assert.equal(streamRes.status, 200, 'streaming request with new key failed');
const route = streamRes.headers.get('x-gateway-route');
await streamRes.text();
console.log(`[E2E] Streaming request authorised with new key, routed to: ${route}`);

// Negative check: a bogus key must be rejected
const bogus = await fetch(`${gwBase}/v1/chat/completions`, {
  method: 'POST',
  headers: { Authorization: 'Bearer sk-definitely-not-valid', 'Content-Type': 'application/json' },
  body: JSON.stringify({ model: testModel.alias, messages: [{ role: 'user', content: 'e2e' }] }),
});
assert.equal(bogus.status, 401, `bogus key should be 401, got ${bogus.status}`);
console.log('[E2E] Bogus key correctly rejected with 401');

// 6. Rollback to baseline
if (baselineVersion > 0) {
  await api('config-versions/rollback', { method: 'POST', body: JSON.stringify({ version: baselineVersion }) });
  const rolledBack = await api('config-versions/publish', { method: 'POST', body: '{}' });
  console.log(`[E2E] Rolled back and published version ${rolledBack.version}`);
  await sleep(5000);

  // 7. Verify rollback adoption
  const afterRollback = await api('config-versions');
  const latest = afterRollback[0];
  assert.ok(latest.source_version === baselineVersion || latest.version === baselineVersion, 'rollback did not restore baseline');
  console.log(`[E2E] Rollback verified: latest version ${latest.version}, source ${latest.source_version ?? 'n/a'}`);
}

// 8. Cleanup
await api(`models/${testModel.id}`, {
  method: 'PATCH',
  body: JSON.stringify({ accounts: testModel.accounts }),
});
if (!testAccount) {
  await api(`accounts/${accountId}`, { method: 'DELETE' });
  console.log('[E2E] Cleaned up test account');
}

console.log('[E2E] ✓ All phases passed: create → publish → adopt → request → rollback → verify');
