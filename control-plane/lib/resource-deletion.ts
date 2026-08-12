import { ApiError } from './api.ts';

type Queryable = { query: (sql: string, params?: unknown[]) => Promise<{ rows: any[]; rowCount?: number | null }> };

export async function deleteAccountResource(client: Queryable, accountId: string) {
  const account = await client.query('SELECT id, provider_id, secret_record_id FROM accounts WHERE id=$1 FOR UPDATE', [accountId]);
  if (!account.rows[0]) throw new ApiError(404, 'not_found', 'Account not found');
  const secretId = account.rows[0].secret_record_id as string | null;
  await client.query('DELETE FROM provider_operations WHERE account_id=$1', [accountId]);
  await client.query('DELETE FROM model_account_mappings WHERE account_id=$1', [accountId]);
  await client.query('DELETE FROM accounts WHERE id=$1', [accountId]);
  await client.query(`UPDATE model_aliases ma SET enabled=false, updated_at=now()
    WHERE ma.enabled AND ma.provider_id IS NULL
      AND NOT EXISTS (SELECT 1 FROM model_account_mappings mam WHERE mam.model_alias_id=ma.id)`);
  await client.query(`UPDATE model_aliases ma SET enabled=false, updated_at=now()
    WHERE ma.enabled AND ma.provider_id=$1
      AND NOT EXISTS (
        SELECT 1 FROM accounts a JOIN discovered_models dm ON dm.account_id=a.id AND dm.provider_id=a.provider_id
        WHERE a.provider_id=ma.provider_id AND a.enabled=true AND dm.upstream_model=ma.upstream_model AND dm.available=true
      )`, [account.rows[0].provider_id]);
  if (secretId) await client.query('DELETE FROM secret_records WHERE id=$1', [secretId]);
  return { id: accountId, deleted: true };
}

export async function deleteProviderResource(client: Queryable, providerId: string) {
  const provider = await client.query('SELECT id FROM providers WHERE id=$1 FOR UPDATE', [providerId]);
  if (!provider.rows[0]) throw new ApiError(404, 'not_found', 'Provider not found');
  const accounts = await client.query('SELECT id FROM accounts WHERE provider_id=$1 ORDER BY id FOR UPDATE', [providerId]);
  for (const account of accounts.rows) await deleteAccountResource(client, account.id);
  await client.query('DELETE FROM providers WHERE id=$1', [providerId]);
  return { id: providerId, deleted: true, deleted_accounts: accounts.rows.length };
}
