import crypto from 'node:crypto';
import type { PoolClient } from 'pg';
import { audit, insertSecret, storeDraft } from '../control';
import type { ClaudeTokenData } from './oauth';

const ANTHROPIC_BASE_URL = 'https://api.anthropic.com';

export type CreateClaudeOAuthAccountInput = {
  token: ClaudeTokenData;
  displayName?: string;
  priority?: number;
  maxConcurrency?: number;
};

function safeNamePart(value: string) {
  return value.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 48) || 'account';
}

// ensureAnthropicProvider finds or creates the anthropic provider for
// api.anthropic.com OAuth accounts (browser login).
async function ensureAnthropicProvider(client: PoolClient): Promise<string> {
  const existing = (await client.query('SELECT id FROM providers WHERE adapter=$1 AND base_url=$2 ORDER BY created_at LIMIT 1', ['anthropic', ANTHROPIC_BASE_URL])).rows[0];
  if (existing?.id) return existing.id as string;
  const created = (await client.query('INSERT INTO providers (slug,name,adapter,base_url,enabled,metadata) VALUES ($1,$2,$3,$4,true,$5) RETURNING id', ['anthropic', 'Anthropic', 'anthropic', ANTHROPIC_BASE_URL, JSON.stringify({})])).rows[0];
  return created.id as string;
}

export async function createClaudeOAuthAccount(client: PoolClient, input: CreateClaudeOAuthAccountInput): Promise<{ id: string; revision: number }> {
  const token = input.token;
  const providerId = await ensureAnthropicProvider(client);
  const emailName = token.email ? safeNamePart(token.email) : '';
  const accountName = token.account_uuid ? `claude-${safeNamePart(token.account_uuid)}` : (emailName ? `claude-${emailName}` : `claude-${crypto.randomUUID().slice(0, 12)}`);
  const displayName = input.displayName?.trim() || (token.email ? `Claude ${token.email}` : 'Claude account');

  const secretId = await insertSecret(client, 'claude-oauth', {
    kind: 'oauth',
    access_token: token.access_token,
    refresh_token: token.refresh_token,
    expires_at: token.expires_at,
    client_id: '9d1c250a-e61b-44d9-88ed-5944d1962f5e',
    revision: 1,
  });
  const metadata = JSON.stringify({ provider: 'anthropic', auth_method: 'browser' });
  const row = (await client.query(`INSERT INTO accounts
    (provider_id, name, display_name, base_url, enabled, priority, weight, max_concurrency, cost, secret_record_id, external_account_id, account_email, token_expires_at, credential_persistence_status, credential_revision, metadata)
    VALUES ($1, $2, $3, $4, true, $5, 1, $6, 0, $7, $8, $9, $10, 'persisted', 1, $11)
    ON CONFLICT (name) DO UPDATE SET secret_record_id=EXCLUDED.secret_record_id, display_name=EXCLUDED.display_name, enabled=true, token_expires_at=EXCLUDED.token_expires_at, credential_persistence_status='persisted', credential_revision=COALESCE(accounts.credential_revision, 0)+1, metadata=EXCLUDED.metadata, updated_at=now()
    RETURNING id, credential_revision`, [
    providerId, accountName, displayName, ANTHROPIC_BASE_URL,
    input.priority ?? 1, input.maxConcurrency ?? 1, secretId,
    token.account_uuid ?? null, token.email ?? null, token.expires_at, metadata,
  ])).rows[0];
  if (!row?.id) throw new Error('claude_account_upsert_failed');

  await storeDraft(client);
  await audit(client, 'claude_account_upsert', 'account', row.id, { method: 'browser', email: token.email ?? null, revision: Number(row.credential_revision) });
  return { id: String(row.id), revision: Number(row.credential_revision) };
}
