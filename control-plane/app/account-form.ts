export type ProviderEndpoint = { id: string; base_url: string };

export function accountDefaultsForProvider(providers: ProviderEndpoint[], providerId: string) {
  const provider = providers.find(item => item.id === providerId);
  return provider ? { providerId: provider.id, baseURL: provider.base_url } : { providerId: '', baseURL: '' };
}
