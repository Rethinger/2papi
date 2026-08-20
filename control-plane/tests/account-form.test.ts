import assert from 'node:assert/strict';
import test from 'node:test';
import { accountDefaultsForProvider } from '../app/account-form.ts';

test('account form inherits the exact endpoint of the selected provider', () => {
  const providers = [
    { id: 'provider-a', base_url: 'https://api.example.test/v1', adapter: 'openai-compatible' },
    { id: 'provider-test', base_url: 'https://tokenharbor.ai/v1', adapter: 'openai-compatible' },
  ];
  assert.deepEqual(accountDefaultsForProvider(providers, 'provider-test'), {
    providerId: 'provider-test',
    baseURL: 'https://tokenharbor.ai/v1',
    adapter: 'openai-compatible',
    credentialKind: 'api_key',
  });
  assert.deepEqual(accountDefaultsForProvider(providers, 'missing'), { providerId: '', baseURL: '', adapter: '', credentialKind: 'api_key' });
});

test('anthropic providers default to api_key credential method', () => {
  const providers = [
    { id: 'claude-provider', base_url: 'https://claude.ai', adapter: 'anthropic' },
  ];
  assert.deepEqual(accountDefaultsForProvider(providers, 'claude-provider'), {
    providerId: 'claude-provider',
    baseURL: 'https://claude.ai',
    adapter: 'anthropic',
    credentialKind: 'api_key',
  });
});
