export type ProviderEndpoint = { id: string; base_url: string; adapter?: string };

export type AccountDraft = {
  providerId: string;
  baseURL: string;
  adapter: string;
  credentialKind: 'api_key' | 'cookie' | 'oauth';
};

export function accountDefaultsForProvider(providers: ProviderEndpoint[], providerId: string): AccountDraft {
  const provider = providers.find(item => item.id === providerId);
  if (!provider) return { providerId: '', baseURL: '', adapter: '', credentialKind: 'api_key' };
  const adapter = provider.adapter ?? 'openai-compatible';
  return {
    providerId: provider.id,
    baseURL: provider.base_url,
    adapter,
    credentialKind: adapter === 'anthropic' ? 'api_key' : 'api_key',
  };
}
