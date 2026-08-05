import { z } from 'zod';
import { CODEX_CLIENT_ID } from './constants';
import { verifyOpenAIIDToken } from './jwt';
import { refreshOAuthCredential } from './oauth';

export type NormalizedCodexCredential = {
  kind: 'oauth'; access_token: string; refresh_token?: string; id_token?: string;
  expires_at: string; client_id: string; chatgpt_account_id: string;
  email?: string; plan_type?: string; auth_method: 'browser'|'device'|'auth_file';
};

const AuthSchema = z.object({
  access_token: z.string().min(1).optional(),
  refresh_token: z.string().min(1).optional(),
  id_token: z.string().min(1).optional(),
  expires_at: z.string().optional(),
  expiresAt: z.string().optional(),
  client_id: z.string().optional(),
  account_id: z.string().optional(),
  chatgpt_account_id: z.string().optional(),
  email: z.string().email().optional(),
  plan_type: z.string().optional(),
}).strict();

function expiry(input: z.infer<typeof AuthSchema>) { return input.expires_at ?? input.expiresAt; }
function isExpired(iso?: string) { return !iso || Number.isNaN(Date.parse(iso)) || Date.parse(iso) <= Date.now(); }

async function normalize(data: z.infer<typeof AuthSchema>, nonce = ''): Promise<NormalizedCodexCredential> {
  if (!data.access_token && !data.refresh_token && !data.id_token) throw new Error('auth_file_missing_oauth_tokens');
  if (!data.access_token && data.refresh_token) data.access_token = 'refresh-required';
  let id = data.chatgpt_account_id ?? data.account_id;
  let email = data.email;
  let plan = data.plan_type;
  if (data.id_token && !isExpired(expiry(data))) {
    const identity = await verifyOpenAIIDToken(data.id_token, nonce).catch(() => undefined);
    id = id ?? identity?.chatgpt_account_id;
    email = email ?? identity?.email;
    plan = plan ?? identity?.plan_type;
  }
  if (isExpired(expiry(data))) {
    if (!data.refresh_token) throw new Error('auth_file_expired_without_refresh_token');
    const refreshed = await refreshOAuthCredential(data.refresh_token, data.client_id ?? CODEX_CLIENT_ID);
    data.access_token = String(refreshed.access_token ?? '');
    data.refresh_token = typeof refreshed.refresh_token === 'string' ? refreshed.refresh_token : data.refresh_token;
    data.id_token = typeof refreshed.id_token === 'string' ? refreshed.id_token : data.id_token;
    data.expires_at = new Date(Date.now() + (typeof refreshed.expires_in === 'number' ? refreshed.expires_in : 3600) * 1000).toISOString();
    if (data.id_token) {
      const identity = await verifyOpenAIIDToken(data.id_token, nonce);
      id = id ?? identity.chatgpt_account_id;
      email = email ?? identity.email;
      plan = plan ?? identity.plan_type;
    }
  }
  if (!data.access_token) throw new Error('auth_file_missing_access_token');
  if (!id) throw new Error('auth_file_missing_account_id');
  return { kind: 'oauth', access_token: data.access_token, refresh_token: data.refresh_token, id_token: data.id_token, expires_at: expiry(data)!, client_id: data.client_id ?? CODEX_CLIENT_ID, chatgpt_account_id: id, email, plan_type: plan, auth_method: 'auth_file' };
}

export async function parseCodexAuthFile(raw: string, nonce = ''): Promise<NormalizedCodexCredential> {
  let parsed: unknown;
  try { parsed = JSON.parse(raw); } catch { throw new Error('auth_file_malformed_json'); }
  if (parsed && typeof parsed === 'object' && 'OPENAI_API_KEY' in parsed) throw new Error('auth_file_api_key_only_unsupported');
  const result = AuthSchema.safeParse(parsed);
  if (!result.success) throw new Error('auth_file_unknown_or_invalid_fields');
  return normalize(result.data, nonce);
}
