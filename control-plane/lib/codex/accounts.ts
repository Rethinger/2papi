import type { NormalizedCodexCredential } from './auth-file';

const SECRET_KEYS = new Set(['access_token', 'refresh_token', 'id_token', 'authorization', 'api_key', 'code', 'code_verifier']);

export function redactCodexSecrets(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(redactCodexSecrets);
  if (value && typeof value === 'object') {
    return Object.fromEntries(Object.entries(value as Record<string, unknown>).map(([k, v]) => [k, SECRET_KEYS.has(k.toLowerCase()) ? '[REDACTED]' : redactCodexSecrets(v)]));
  }
  return value;
}

export type CodexAccountInput = {
  name: string;
  credential: NormalizedCodexCredential;
  enabled?: boolean;
  max_concurrency?: number;
};

export function toCodexRuntimeCredential(credential: NormalizedCodexCredential, revision: number) {
  return {
    kind: 'oauth' as const,
    access_token: credential.access_token,
    refresh_token: credential.refresh_token,
    id_token: credential.id_token,
    expires_at: credential.expires_at,
    client_id: credential.client_id,
    chatgpt_account_id: credential.chatgpt_account_id,
    revision,
  };
}
