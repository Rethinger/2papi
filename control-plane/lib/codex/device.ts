import crypto from 'node:crypto';
import { leaseJsonWhenDue, setJsonUntilMs, updateLeasedJson } from '../redis';
import { CODEX_CLIENT_ID, codexUrls } from './constants';
import { verifyOpenAIIDToken } from './jwt';
import type { NormalizedCodexCredential } from './auth-file';

export type DeviceFlowState = 'pending' | 'slow_down' | 'expired' | 'denied' | 'failed' | 'complete';
function key(id: string) { return `codex:device:${id}`; }
type StoredDeviceFlow = { nonce: string; device_code: string; interval: number; next_poll_at_ms: number; expires_at_ms: number; created_at: string; lease_until_ms?: number };

export async function startDeviceFlow(ttlSeconds = 900) {
  const nonce = crypto.randomBytes(24).toString('base64url');
  const res = await fetch(codexUrls().deviceUserCode, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ client_id: CODEX_CLIENT_ID, nonce }) });
  if (!res.ok) throw new Error('device_flow_start_failed');
  const body = await res.json() as Record<string, unknown>;
  const device_code = String(body.device_code ?? '');
  if (!device_code) throw new Error('device_flow_missing_code');
  const session = crypto.randomBytes(16).toString('base64url');
  const expiresIn = Number(body.expires_in ?? ttlSeconds);
  const interval = Number(body.interval ?? 5);
  if (!Number.isFinite(expiresIn) || expiresIn <= 0) throw new Error('device_flow_invalid_expiry');
  if (!Number.isFinite(interval) || interval < 0) throw new Error('device_flow_invalid_interval');
  const expiresAtMs = Date.now() + expiresIn * 1000;
  await setJsonUntilMs(key(session), { nonce, device_code, interval, next_poll_at_ms: Date.now(), expires_at_ms: expiresAtMs, created_at: new Date().toISOString() }, expiresAtMs);
  return { session, user_code: String(body.user_code ?? ''), verification_uri: String(body.verification_uri ?? codexUrls().deviceVerification), expires_in: expiresIn, interval };
}

export async function pollDeviceFlow(session: string): Promise<{ state: DeviceFlowState; credential?: NormalizedCodexCredential; interval?: number }> {
  const due = await leaseJsonWhenDue<StoredDeviceFlow>(key(session), 30000);
  if (due.status === 'missing') return { state: 'expired' };
  if (due.status === 'too_soon') return { state: 'slow_down', interval: due.value?.interval };
  const stored = due.value!;
  const expiresAtMs = Math.min(stored.expires_at_ms, due.expiresAtMs ?? stored.expires_at_ms);
  let res: Response;
  let body: Record<string, unknown>;
  try {
    res = await fetch(codexUrls().deviceToken, { method: 'POST', headers: { 'content-type': 'application/x-www-form-urlencoded' }, body: new URLSearchParams({ grant_type: 'urn:ietf:params:oauth:grant-type:device_code', client_id: CODEX_CLIENT_ID, device_code: stored.device_code }) });
    body = await res.json().catch(() => ({})) as Record<string, unknown>;
  } catch {
    await updateLeasedJson(key(session), { ...stored, next_poll_at_ms: Date.now() + stored.interval * 1000 }, expiresAtMs);
    return { state: 'failed' };
  }
  if (!res.ok) {
    const err = String(body.error ?? 'authorization_pending');
    if (err === 'authorization_pending') { const interval = stored.interval; await updateLeasedJson(key(session), { ...stored, interval, next_poll_at_ms: Date.now() + interval * 1000 }, expiresAtMs); return { state: 'pending', interval }; }
    if (err === 'slow_down') { const interval = Number(body.interval ?? stored.interval + 5); await updateLeasedJson(key(session), { ...stored, interval, next_poll_at_ms: Date.now() + interval * 1000 }, expiresAtMs); return { state: 'slow_down', interval }; }
    if (err === 'expired_token') { await updateLeasedJson(key(session), stored, expiresAtMs, 'delete'); return { state: 'expired' }; }
    if (err === 'access_denied') { await updateLeasedJson(key(session), stored, expiresAtMs, 'delete'); return { state: 'denied' }; }
    await updateLeasedJson(key(session), { ...stored, next_poll_at_ms: Date.now() + stored.interval * 1000 }, expiresAtMs);
    return { state: 'failed' };
  }
  const idToken = typeof body.id_token === 'string' ? body.id_token : undefined;
  if (!idToken) { await updateLeasedJson(key(session), { ...stored, next_poll_at_ms: Date.now() + stored.interval * 1000 }, expiresAtMs); return { state: 'failed' }; }
  let identity;
  try { identity = await verifyOpenAIIDToken(idToken, stored.nonce); } catch { await updateLeasedJson(key(session), { ...stored, next_poll_at_ms: Date.now() + stored.interval * 1000 }, expiresAtMs); return { state: 'failed' }; }
  if (typeof body.access_token !== 'string' || !body.access_token) { await updateLeasedJson(key(session), { ...stored, next_poll_at_ms: Date.now() + stored.interval * 1000 }, expiresAtMs); return { state: 'failed' }; }
  await updateLeasedJson(key(session), stored, expiresAtMs, 'delete');
  return { state: 'complete', credential: { kind: 'oauth', access_token: body.access_token, refresh_token: typeof body.refresh_token === 'string' ? body.refresh_token : undefined, id_token: idToken, expires_at: new Date(Date.now() + Number(body.expires_in ?? 3600) * 1000).toISOString(), client_id: CODEX_CLIENT_ID, chatgpt_account_id: identity.chatgpt_account_id, email: identity.email, plan_type: identity.plan_type, auth_method: 'device' } };
}
