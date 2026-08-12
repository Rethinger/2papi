import assert from 'node:assert/strict';
import test from 'node:test';
import { accountDefaultsForProvider } from '../app/account-form.ts';

test('account form inherits the exact endpoint of the selected provider', () => {
  const providers = [
    { id: 'provider-a', base_url: 'https://api.example.test/v1' },
    { id: 'provider-test', base_url: 'https://tokenharbor.ai/v1' },
  ];
  assert.deepEqual(accountDefaultsForProvider(providers, 'provider-test'), {
    providerId: 'provider-test',
    baseURL: 'https://tokenharbor.ai/v1',
  });
  assert.deepEqual(accountDefaultsForProvider(providers, 'missing'), { providerId: '', baseURL: '' });
});
