import { z } from 'zod';
import { CODEX_CLIENT_ID } from './constants';
import { unsafeReadJwtExp, verifyOpenAIAccessToken, verifyOpenAIIDToken, type VerifiedCodexIdentity } from './jwt';
import { refreshOAuthCredential } from './oauth';

export type NormalizedCodexCredential = {
  kind: 'oauth'; access_token: string; refresh_token?: string; id_token?: string;
  expires_at: string; client_id: string; chatgpt_account_id: string;
  email?: string; plan_type?: string; auth_method: 'browser'|'device'|'auth_file';
};

const TokenSchema = z.object({
  access_token: z.string().min(1).optional(),
  refresh_token: z.string().min(1).optional(),
  id_token: z.string().min(1).optional(),
  account_id: z.string().min(1).optional(),
  expires_at: z.string().optional(),
  expiresAt: z.string().optional(),
}).strict();

const HintSchema = z.object({
  client_id: z.string().optional(),
  account_id: z.string().optional(),
  chatgpt_account_id: z.string().optional(),
  email: z.string().email().optional(),
  plan_type: z.string().optional(),
}).strict();

const FlatAuthSchema = TokenSchema.merge(HintSchema);
const NestedAuthSchema = HintSchema.extend({ OPENAI_API_KEY: z.null().optional(), last_refresh: z.string().optional(), tokens: TokenSchema }).strict();
const CurrentAuthSchema = z.object({
  auth_mode: z.string().min(1),
  openai_api_key: z.null().optional(),
  tokens: TokenSchema,
  last_refresh: z.string().optional(),
  agent_identity: z.null().optional(),
  personal_access_token: z.null().optional(),
  bedrock_api_key: z.null().optional(),
  _meta: z.record(z.string(), z.unknown()).optional(),
}).strict();
type AuthData = z.infer<typeof FlatAuthSchema>;

function expiry(input: Pick<AuthData, 'expires_at'|'expiresAt'>) { return input.expires_at ?? input.expiresAt; }
function isExpired(iso?: string) { return !iso || Number.isNaN(Date.parse(iso)) || Date.parse(iso) <= Date.now(); }
function hintId(data: AuthData) { return data.chatgpt_account_id ?? data.account_id; }

function applyIdentity(data: AuthData, identity: VerifiedCodexIdentity) {
  const hinted = hintId(data);
  if (hinted && hinted !== identity.chatgpt_account_id) throw new Error('auth_file_identity_mismatch');
  return { id: identity.chatgpt_account_id, email: identity.email, plan: identity.plan_type };
}

function idTokenExpired(idToken?: string) {
  if (!idToken) return true;
  const exp = unsafeReadJwtExp(idToken);
  return typeof exp !== 'number' || exp <= Math.floor(Date.now() / 1000);
}

async function accessIdentity(data: AuthData) {
  if (!data.access_token) return undefined;
  try { return applyIdentity(data, await verifyOpenAIAccessToken(data.access_token)); }
  catch { return undefined; }
}

async function normalize(data: AuthData, nonce?: string): Promise<NormalizedCodexCredential> {
  if (!data.access_token && !data.refresh_token && !data.id_token) throw new Error('auth_file_missing_oauth_tokens');
  if (!expiry(data) && data.access_token) {
    const accessExp = unsafeReadJwtExp(data.access_token);
    if (typeof accessExp === 'number') data.expires_at = new Date(accessExp * 1000).toISOString();
  }
  let identity: ReturnType<typeof applyIdentity> | undefined;
  const accessExpired = isExpired(expiry(data));
  if (data.id_token && !idTokenExpired(data.id_token)) identity = applyIdentity(data, await verifyOpenAIIDToken(data.id_token, nonce));
  else if (!accessExpired) identity = await accessIdentity(data);
  const needsRefresh = accessExpired || !identity;

  if (needsRefresh) {
    if (!data.refresh_token) throw new Error('auth_file_expired_without_refresh_token');
    const refreshed = await refreshOAuthCredential(data.refresh_token, data.client_id ?? CODEX_CLIENT_ID);
    data.access_token = typeof refreshed.access_token === 'string' ? refreshed.access_token : '';
    data.refresh_token = typeof refreshed.refresh_token === 'string' ? refreshed.refresh_token : data.refresh_token;
    data.id_token = typeof refreshed.id_token === 'string' ? refreshed.id_token : data.id_token;
    data.expires_at = new Date(Date.now() + (typeof refreshed.expires_in === 'number' ? refreshed.expires_in : 3600) * 1000).toISOString();
    if (data.id_token && !idTokenExpired(data.id_token)) identity = applyIdentity(data, await verifyOpenAIIDToken(data.id_token, nonce));
    else if (!isExpired(expiry(data))) identity = await accessIdentity(data);
  }

  if (!data.access_token) throw new Error('auth_file_missing_access_token');
  if (!identity) throw new Error('auth_file_missing_verified_identity');
  return { kind: 'oauth', access_token: data.access_token, refresh_token: data.refresh_token, id_token: data.id_token, expires_at: expiry(data)!, client_id: data.client_id ?? CODEX_CLIENT_ID, chatgpt_account_id: identity.id, email: identity.email, plan_type: identity.plan, auth_method: 'auth_file' };
}

export async function parseCodexAuthFile(raw: string, nonce?: string): Promise<NormalizedCodexCredential> {
  let parsed: unknown;
  try { parsed = JSON.parse(raw); } catch { throw new Error('auth_file_malformed_json'); }
  if (parsed && typeof parsed === 'object' && 'OPENAI_API_KEY' in parsed && (parsed as { OPENAI_API_KEY?: unknown }).OPENAI_API_KEY !== null) throw new Error('auth_file_api_key_only_unsupported');
  if (parsed && typeof parsed === 'object' && 'openai_api_key' in parsed && (parsed as { openai_api_key?: unknown }).openai_api_key !== null) throw new Error('auth_file_api_key_only_unsupported');
  const current = CurrentAuthSchema.safeParse(parsed);
  if (current.success) return normalize(current.data.tokens, nonce);
  const nested = NestedAuthSchema.safeParse(parsed);
  if (nested.success) return normalize({ ...nested.data, ...nested.data.tokens }, nonce);
  const flat = FlatAuthSchema.safeParse(parsed);
  if (flat.success) return normalize(flat.data, nonce);
  throw new Error('auth_file_unknown_or_invalid_fields');
}
