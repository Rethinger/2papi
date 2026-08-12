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
} from '../lib/control.ts';

test('management schemas accept valid payloads', () => {
  assert.equal(AccountSchema.parse({ provider_id: '00000000-0000-0000-0000-000000000000', name: 'a', display_name: 'A', base_url: 'https://example.com', credential: { api_key: 'sk' } }).weight, 1);
  assert.equal(ModelSchema.parse({ alias: 'gpt-dev', upstream_model: 'gpt-4o-mini', accounts: ['00000000-0000-0000-0000-000000000000'] }).alias, 'gpt-dev');
  assert.equal(RoutingSchema.parse({}).strategy, 'balanced');
  assert.equal(VirtualKeySchema.parse({ name: 'dev', plaintext_key: 'sk-gateway-dev' }).rpm, 60);
});

test('management schemas reject unsafe payloads', () => {
  assert.throws(() => AccountSchema.parse({ provider_id: 'bad', name: '', display_name: '', base_url: 'nope' }));
  assert.throws(() => ModelSchema.parse({ alias: 'x', upstream_model: 'y', accounts: [] }));
  assert.throws(() => RoutingSchema.parse({ max_attempts: 0 }));
});

test('patch schemas preserve omission instead of applying create defaults', () => {
  assert.deepEqual(AccountPatchSchema.parse({ priority: 7 }), { priority: 7 });
  assert.deepEqual(AccountPatchSchema.parse({ credential: { api_key: 'rotated' } }), { credential: { api_key: 'rotated' } });
  assert.deepEqual(ProviderPatchSchema.parse({ name: 'Renamed' }), { name: 'Renamed' });
  assert.deepEqual(ModelPatchSchema.parse({ enabled: false }), { enabled: false });
  assert.deepEqual(VirtualKeyPatchSchema.parse({ rpm: 120 }), { rpm: 120 });
});
