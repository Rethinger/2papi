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

const defaults: CreateCodexAccountDeps = { insertSecret: defaultInsertSecret, audit: defaultAudit, storeDraft: defaultStoreDraft };

function accountName(input: CreateCodexAccountInput) {
  return input.name ?? `codex-${input.credential.chatgpt_account_id}`;
}

export async function createCodexAccount(client: PoolClient, input: CreateCodexAccountInput, deps: CreateCodexAccountDeps = defaults) {
  const credential = input.credential;
  const secretId = await deps.insertSecret(client, 'codex-oauth', credential);
  const name = accountName(input);
  const displayName = credential.email ? `Codex ${credential.email}` : name;
  const enabled = input.enabled ?? true;
  const maxConcurrency = input.max_concurrency ?? 1;

  const sql = input.accountId
    ? `UPDATE accounts SET secret_record_id=$1, display_name=$2, enabled=$3, max_concurrency=$4, external_account_id=$5, account_email=$6, plan_type=$7, token_expires_at=$8, last_credential_refresh_at=now(), credential_persistence_status='persisted', metadata=$9, updated_at=now() WHERE id=$10 RETURNING id, name`
    : `INSERT INTO accounts (provider_id, name, display_name, base_url, enabled, priority, weight, max_concurrency, cost, secret_record_id, external_account_id, account_email, plan_type, token_expires_at, credential_persistence_status, metadata) VALUES ((SELECT id FROM providers WHERE slug='openai-codex' LIMIT 1), $1, $2, 'https://chatgpt.com', $3, 1, 1, $4, 0, $5, $6, $7, $8, $9, 'persisted', $10) ON CONFLICT (name) DO UPDATE SET secret_record_id=EXCLUDED.secret_record_id, display_name=EXCLUDED.display_name, enabled=EXCLUDED.enabled, max_concurrency=EXCLUDED.max_concurrency, external_account_id=EXCLUDED.external_account_id, account_email=EXCLUDED.account_email, plan_type=EXCLUDED.plan_type, token_expires_at=EXCLUDED.token_expires_at, credential_persistence_status='persisted', metadata=EXCLUDED.metadata, updated_at=now() RETURNING id, name`;
  const metadata = JSON.stringify({ provider: 'openai-codex', auth_method: input.method });
  const params = input.accountId
    ? [secretId, displayName, enabled, maxConcurrency, credential.chatgpt_account_id, credential.email ?? null, credential.plan_type ?? null, credential.expires_at ?? null, metadata, input.accountId]
    : [name, displayName, enabled, maxConcurrency, secretId, credential.chatgpt_account_id, credential.email ?? null, credential.plan_type ?? null, credential.expires_at ?? null, metadata];
  const row = (await client.query(sql, params)).rows[0];
  const draft = await deps.storeDraft(client);
  const revision = Number(draft.version);
  await deps.audit(client, 'codex_account_upsert', 'account', row.id, { account_id: row.id, method: input.method, plan: credential.plan_type ?? null, revision });
  return { id: String(row.id), revision };
}
