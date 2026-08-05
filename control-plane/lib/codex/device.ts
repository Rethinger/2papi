import crypto from 'node:crypto';
import { consumeJsonOnce, setJsonWithTtl } from '../redis';
import { CODEX_CLIENT_ID, codexUrls } from './constants';
import { verifyOpenAIIDToken } from './jwt';
import type { NormalizedCodexCredential } from './auth-file';

export type DeviceFlowState = 'pending' | 'authorized' | 'slow_down' | 'expired' | 'denied';
function key(id: string) { return `codex:device:${id}`; }

export async function startDeviceFlow(ttlSeconds = 900) {
  const nonce = crypto.randomBytes(24).toString('base64url');
  const res = await fetch(codexUrls().deviceUserCode, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ client_id: CODEX_CLIENT_ID, nonce }) });
  if (!res.ok) throw new Error('device_flow_start_failed');
  const body = await res.json() as Record<string, unknown>;
  const device_code = String(body.device_code ?? '');
  if (!device_code) throw new Error('device_flow_missing_code');
  const session = crypto.randomBytes(16).toString('base64url');
  await setJsonWithTtl(key(session), { nonce, device_code, created_at: new Date().toISOString() }, ttlSeconds);
  return { session, device_code, user_code: String(body.user_code ?? ''), verification_uri: String(body.verification_uri ?? codexUrls().deviceVerification), expires_in: Number(body.expires_in ?? ttlSeconds), interval: Number(body.interval ?? 5) };
}

export async function pollDeviceFlow(session: string): Promise<{ state: DeviceFlowState; credential?: NormalizedCodexCredential; interval?: number }> {
  const stored = await consumeJsonOnce<{ nonce: string; device_code: string }>(key(session));
  if (!stored) return { state: 'expired' };
  const res = await fetch(codexUrls().deviceToken, { method: 'POST', headers: { 'content-type': 'application/x-www-form-urlencoded' }, body: new URLSearchParams({ grant_type: 'urn:ietf:params:oauth:grant-type:device_code', client_id: CODEX_CLIENT_ID, device_code: stored.device_code }) });
  const body = await res.json().catch(() => ({})) as Record<string, unknown>;
  if (!res.ok) {
    const err = String(body.error ?? 'authorization_pending');
    if (err === 'authorization_pending') { await setJsonWithTtl(key(session), stored, 900); return { state: 'pending' }; }
    if (err === 'slow_down') { await setJsonWithTtl(key(session), stored, 900); return { state: 'slow_down', interval: Number(body.interval ?? 10) }; }
    if (err === 'expired_token') return { state: 'expired' };
    return { state: 'denied' };
  }
  const idToken = typeof body.id_token === 'string' ? body.id_token : undefined;
  const identity = idToken ? await verifyOpenAIIDToken(idToken, stored.nonce) : undefined;
  if (typeof body.access_token !== 'string') throw new Error('device_flow_missing_access_token');
  return { state: 'authorized', credential: { kind: 'oauth', access_token: body.access_token, refresh_token: typeof body.refresh_token === 'string' ? body.refresh_token : undefined, id_token: idToken, expires_at: new Date(Date.now() + Number(body.expires_in ?? 3600) * 1000).toISOString(), client_id: CODEX_CLIENT_ID, chatgpt_account_id: identity?.chatgpt_account_id ?? String(body.account_id ?? ''), email: identity?.email, plan_type: identity?.plan_type, auth_method: 'device' } };
}
