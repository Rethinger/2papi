import { cookies, headers } from 'next/headers';
import { localeCookieName, resolveLocale, type Locale } from './i18n';

export async function getRequestLocale(): Promise<Locale> {
  const [cookieStore, headerStore] = await Promise.all([cookies(), headers()]);
  return resolveLocale(cookieStore.get(localeCookieName)?.value, headerStore.get('accept-language'));
}
