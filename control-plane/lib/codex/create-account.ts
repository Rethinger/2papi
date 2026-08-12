import { randomUUID } from 'node:crypto';
import type { PoolClient } from 'pg';
import type { NormalizedCodexCredential } from './auth-file';
import { audit as defaultAudit, insertSecret as defaultInsertSecret, storeDraft as defaultStoreDraft } from '../control';

export type CreateCodexAccountInput = {
  name?: string;
  accountId?: string;
  method: 'browser' | 'device' | 'import';
  credential: NormalizedCodexCredential;
  enabled?: boolean;
  max_concurrency?: number;
};

export type CreateCodexAccountDeps = {
  insertSecret: typeof defaultInsertSecret;
  audit: typeof defaultAudit;
  storeDraft: typeof defaultStoreDraft;
};

const CODEX_BASE_URL = 'https://chatgpt.com/backend-api/codex';
const defaults: CreateCodexAccountDeps = { insertSecret: defaultInsertSecret, audit: defaultAudit, storeDraft: defaultStoreDraft };

function safeNamePart(value: string) {
  return value
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 80);
}

function generatedSafeName() {
  return `codex-${randomUUID().replace(/-/g, '').slice(0, 12)}`;
}

function accountName(input: CreateCodexAccountInput) {
  if (input.name) return input.name;
  const emailName = input.credential.email ? safeNamePart(input.credential.email) : '';
  if (emailName) return `codex-${emailName}`;
  const accountIdName = input.credential.chatgpt_account_id ? safeNamePart(input.credential.chatgpt_account_id) : '';
  if (accountIdName) return `codex-${accountIdName}`;
  return generatedSafeName();
}

export async function createCodexAccount(client: PoolClient, input: CreateCodexAccountInput, deps: CreateCodexAccountDeps = defaults) {
  const credential = input.credential;
  const provider = (await client.query(`SELECT id FROM providers WHERE slug='openai-codex' LIMIT 1`)).rows[0];
  if (!provider?.id) throw new Error('codex_provider_missing');

  const name = accountName(input);
  const existing = input.accountId
    ? (await client.query('SELECT id, secret_record_id FROM accounts WHERE id=$1 AND provider_id=$2 FOR UPDATE', [input.accountId, provider.id])).rows[0]
    : credential.chatgpt_account_id
      ? (await client.query('SELECT id, secret_record_id FROM accounts WHERE provider_id=$1 AND external_account_id=$2 ORDER BY created_at LIMIT 1 FOR UPDATE', [provider.id, credential.chatgpt_account_id])).rows[0]
      : undefined;
  if (input.accountId && !existing?.id) throw new Error('codex_account_missing');
  const target = existing ?? (await client.query('SELECT id, secret_record_id FROM accounts WHERE provider_id=$1 AND name=$2 FOR UPDATE', [provider.id, name])).rows[0];
  const secretId = await deps.insertSecret(client, 'codex-oauth', credential);
  const displayName = credential.email ? `Codex ${credential.email}` : name;
  const enabled = input.enabled ?? true;
  const maxConcurrency = input.max_concurrency ?? 1;
  const metadata = JSON.stringify({ provider: 'openai-codex', auth_method: input.method });

  const sql = target?.id
    ? `UPDATE accounts SET secret_record_id=$1, display_name=$2, base_url=$3, enabled=$4, max_concurrency=$5, external_account_id=$6, account_email=$7, plan_type=$8, token_expires_at=$9, last_credential_refresh_at=now(), credential_persistence_status='persisted', credential_revision=COALESCE(credential_revision, 0)+1, metadata=$10, updated_at=now() WHERE id=$11 AND provider_id=$12 RETURNING id, name, credential_revision`
    : `INSERT INTO accounts (provider_id, name, display_name, base_url, enabled, priority, weight, max_concurrency, cost, secret_record_id, external_account_id, account_email, plan_type, token_expires_at, credential_persistence_status, credential_revision, metadata) VALUES ($1, $2, $3, $4, $5, 1, 1, $6, 0, $7, $8, $9, $10, $11, 'persisted', 1, $12) ON CONFLICT (name) DO UPDATE SET secret_record_id=EXCLUDED.secret_record_id, display_name=EXCLUDED.display_name, base_url=EXCLUDED.base_url, enabled=EXCLUDED.enabled, max_concurrency=EXCLUDED.max_concurrency, external_account_id=EXCLUDED.external_account_id, account_email=EXCLUDED.account_email, plan_type=EXCLUDED.plan_type, token_expires_at=EXCLUDED.token_expires_at, credential_persistence_status='persisted', credential_revision=COALESCE(accounts.credential_revision, 0)+1, metadata=EXCLUDED.metadata, updated_at=now() WHERE accounts.provider_id=EXCLUDED.provider_id RETURNING id, name, credential_revision`;
  const params = target?.id
    ? [secretId, displayName, CODEX_BASE_URL, enabled, maxConcurrency, credential.chatgpt_account_id ?? null, credential.email ?? null, credential.plan_type ?? null, credential.expires_at ?? null, metadata, target.id, provider.id]
    : [provider.id, name, displayName, CODEX_BASE_URL, enabled, maxConcurrency, secretId, credential.chatgpt_account_id ?? null, credential.email ?? null, credential.plan_type ?? null, credential.expires_at ?? null, metadata];
  const row = (await client.query(sql, params)).rows[0];
  if (!row?.id) throw new Error(target?.id ? 'codex_account_missing' : 'codex_account_upsert_failed');
  if (target?.secret_record_id && target.secret_record_id !== secretId) await client.query('DELETE FROM secret_records WHERE id=$1', [target.secret_record_id]);

  await deps.storeDraft(client);
  const revision = Number(row.credential_revision);
  await deps.audit(client, 'codex_account_upsert', 'account', row.id, { account_id: row.id, method: input.method, plan: credential.plan_type ?? null, revision });
  return { id: String(row.id), revision };
}
