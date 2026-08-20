import test from 'node:test';
import assert from 'node:assert/strict';
import {
  AccountPatchSchema,
  AccountSchema,
  ModelPatchSchema,
  ModelSchema,
  ProviderPatchSchema,
  RoutingSchema,
  VirtualKeyPatchSchema,
  VirtualKeySchema,
  WebhookSchema,
  isPublicHttpUrl,
} from '../lib/control.ts';

test('management schemas accept valid payloads', () => {
  assert.equal(AccountSchema.parse({ provider_id: '00000000-0000-0000-0000-000000000000', name: 'a', display_name: 'A', base_url: 'https://example.com', credential: { api_key: 'sk' } }).weight, 1);
  assert.equal(ModelSchema.parse({ alias: 'gpt-dev', upstream_model: 'gpt-4o-mini', accounts: ['00000000-0000-0000-0000-000000000000'] }).alias, 'gpt-dev');
  assert.equal(RoutingSchema.parse({}).strategy, 'balanced');
  const parsedVk = VirtualKeySchema.parse({ name: 'dev', plaintext_key: 'sk-gateway-dev', tpm: 5000, max_concurrency: 4, budget_usd: 12.5 });
  assert.equal(parsedVk.rpm, 60);
  assert.equal(parsedVk.tpm, 5000);
  assert.equal(parsedVk.max_concurrency, 4);
  assert.equal(parsedVk.budget_usd, 12.5);

  const parsedModel = ModelSchema.parse({ alias: 'gpt-dev', upstream_model: 'gpt-4o-mini', accounts: ['00000000-0000-0000-0000-000000000000'], fallbacks: ['gpt-fallback'], input_per_mtok: 0.15, output_per_mtok: 0.60 });
  assert.deepEqual(parsedModel.fallbacks, ['gpt-fallback']);
  assert.equal(parsedModel.input_per_mtok, 0.15);
  assert.equal(parsedModel.output_per_mtok, 0.60);
});

test('account proxy field accepts any format and rejects invalid lists', () => {
  const base = { provider_id: '00000000-0000-0000-0000-000000000000', name: 'a', display_name: 'A', base_url: 'https://example.com', credential: { api_key: 'sk' } };
  const parsed = AccountSchema.parse({ ...base, proxy: 'http://user:pass@host:8080\nsocks5://host:1080' });
  assert.equal(parsed.proxy, 'http://user:pass@host:8080\nsocks5://host:1080');
  assert.equal(AccountSchema.parse({ ...base, proxy: '' }).proxy, '');
  assert.throws(() => AccountSchema.parse({ ...base, proxy: 'host:99999' }));
  assert.throws(() => AccountSchema.parse({ ...base, proxy: 'socks6://nope:1' }));
  // PATCH schema: proxy is optional and stays omitted when absent.
  const patch = AccountPatchSchema.parse({ display_name: 'B' });
  assert.equal('proxy' in patch, false);
  const patchProxy = AccountPatchSchema.parse({ proxy: 'host:8080' });
  assert.equal(patchProxy.proxy, 'host:8080');
});

test('account credentials accept cookie and oauth kinds with kind-specific fields', () => {
  const base = { provider_id: '00000000-0000-0000-0000-000000000000', name: 'a', display_name: 'A', base_url: 'https://claude.ai' };

  const cookie = AccountSchema.parse({ ...base, credential: { kind: 'cookie', cookies: 'sessionKey=sk-ant-x', organization_id: 'org-1' } });
  assert.equal(cookie.credential?.kind, 'cookie');
  assert.equal(cookie.credential?.cookies, 'sessionKey=sk-ant-x');

  const oauth = AccountSchema.parse({ ...base, credential: { kind: 'oauth', access_token: 'sk-ant-oauth' } });
  assert.equal(oauth.credential?.kind, 'oauth');
  assert.equal(oauth.credential?.access_token, 'sk-ant-oauth');

  // Legacy payloads without a kind still parse as api_key.
  const legacy = AccountSchema.parse({ ...base, credential: { api_key: 'sk-ant-key' } });
  assert.equal(legacy.credential?.kind, 'api_key');

  assert.throws(() => AccountSchema.parse({ ...base, credential: { kind: 'cookie' } }));
  assert.throws(() => AccountSchema.parse({ ...base, credential: { kind: 'cookie', api_key: 'sk-x' } }));
  assert.throws(() => AccountSchema.parse({ ...base, credential: { kind: 'oauth' } }));
  assert.throws(() => AccountSchema.parse({ ...base, credential: { kind: 'api_key' } }));
});

test('routing schema carries the optimization flags', () => {
  const parsed = RoutingSchema.parse({});
  assert.deepEqual(parsed.optimization, { rtk_compression: false, caveman: false, headroom: false, headroom_reserve: 120000, headroom_keep: 8 });
  const enabled = RoutingSchema.parse({ optimization: { rtk_compression: true, caveman: true, headroom: true, headroom_reserve: 80000, headroom_keep: 4 } });
  assert.equal(enabled.optimization.rtk_compression, true);
  assert.equal(enabled.optimization.caveman, true);
  assert.equal(enabled.optimization.headroom, true);
  assert.equal(enabled.optimization.headroom_reserve, 80000);
  assert.equal(enabled.optimization.headroom_keep, 4);
  const onlyCaveman = RoutingSchema.parse({ optimization: { caveman: true } });
  assert.equal(onlyCaveman.optimization.caveman, true);
  assert.equal(onlyCaveman.optimization.rtk_compression, false);
  assert.equal(onlyCaveman.optimization.headroom, false);
});

test('SSRF guard accepts public endpoints and rejects private IP literals', () => {
  for (const good of ['https://api.openai.com', 'http://fake-upstream:9001', 'https://claude.ai', 'https://8.8.8.8/v1']) {
    assert.equal(isPublicHttpUrl(good), true, `${good} should be allowed`);
  }
  for (const bad of ['http://127.0.0.1:9001', 'http://localhost:9001', 'http://10.0.0.5/v1', 'http://172.16.0.1', 'http://192.168.1.1', 'http://169.254.169.254/latest/meta-data', 'http://[::1]:8000', 'ftp://api.example.com']) {
    assert.equal(isPublicHttpUrl(bad), false, `${bad} should be blocked`);
  }
  // Private IP literals are rejected by the account/provider schemas.
  const base = { provider_id: '00000000-0000-0000-0000-000000000000', name: 'a', display_name: 'A', base_url: 'http://127.0.0.1:9001' };
  assert.throws(() => AccountSchema.parse({ ...base, credential: { api_key: 'sk' } }));
});

test('webhook schema accepts disabled default and enabled with url', () => {
  assert.deepEqual(WebhookSchema.parse({}), { enabled: false, url: '', secret: '' });
  const enabled = WebhookSchema.parse({ enabled: true, url: 'https://example.com/hook', secret: 'abc' });
  assert.equal(enabled.enabled, true);
  assert.equal(enabled.url, 'https://example.com/hook');
  assert.throws(() => WebhookSchema.parse({ enabled: true, url: 'not-a-url' }));
});

test('management schemas reject unsafe payloads', () => {
  assert.throws(() => AccountSchema.parse({ provider_id: 'bad', name: '', display_name: '', base_url: 'nope' }));
  assert.throws(() => ModelSchema.parse({ alias: 'x', upstream_model: 'y', accounts: [] }));
  assert.throws(() => RoutingSchema.parse({ max_attempts: 0 }));
  assert.throws(() => VirtualKeySchema.parse({ name: 'dev', budget_usd: -5 }));
  assert.throws(() => ModelSchema.parse({ alias: 'x', upstream_model: 'y', accounts: ['00000000-0000-0000-0000-000000000000'], input_per_mtok: -1 }));
});

test('patch schemas preserve omission instead of applying create defaults', () => {
  assert.deepEqual(AccountPatchSchema.parse({ priority: 7 }), { priority: 7 });
  assert.deepEqual(AccountPatchSchema.parse({ credential: { api_key: 'rotated', kind: 'api_key' } }), { credential: { api_key: 'rotated', kind: 'api_key' } });
  assert.deepEqual(ProviderPatchSchema.parse({ name: 'Renamed' }), { name: 'Renamed' });
  assert.deepEqual(ModelPatchSchema.parse({ enabled: false }), { enabled: false });
  assert.deepEqual(VirtualKeyPatchSchema.parse({ rpm: 120 }), { rpm: 120 });
  assert.deepEqual(VirtualKeyPatchSchema.parse({ budget_usd: 50, tpm: 10000, max_concurrency: 5 }), { budget_usd: 50, tpm: 10000, max_concurrency: 5 });
  assert.deepEqual(ModelPatchSchema.parse({ fallbacks: ['fb1', 'fb2'], input_per_mtok: 0.2, output_per_mtok: 0.8 }), { fallbacks: ['fb1', 'fb2'], input_per_mtok: 0.2, output_per_mtok: 0.8 });
});
