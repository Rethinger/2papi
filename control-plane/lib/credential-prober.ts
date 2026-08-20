import { ApiError } from './api';
import { audit } from './control';
import type { Queryable } from './db';
import { dispatchProviderOperation, type OperationKind } from './provider-operations';

type Dispatch = (
  client: Queryable,
  accountID: string,
  kind: OperationKind,
  input: unknown,
  idempotencyKey?: string,
) => Promise<{ data: unknown; warning_code?: string }>;

export type ProbeResult = { account_id: string; account_name: string; status: 'ok' | 'failed'; error_code: string | null };

export type ProbeDeps = { dispatch?: Dispatch; concurrency?: number };

export async function probeAccountCredentials(client: Queryable, accountID: string, deps: ProbeDeps = {}): Promise<ProbeResult> {
  const dispatch = deps.dispatch ?? dispatchProviderOperation;
  const account = await client.query('SELECT name FROM accounts WHERE id=$1 AND enabled=true', [accountID]);
  if (!account.rows[0]) throw new ApiError(404, 'account_not_found', 'Enabled account not found');
  try {
    await dispatch(client, accountID, 'validate_credentials', {}, `credential-probe:${accountID}:${Date.now()}`);
    await client.query(`INSERT INTO account_provider_state (account_id, last_operation, last_error_code, last_error_message, updated_at)
      VALUES ($1, 'credential.probe', NULL, NULL, now())
      ON CONFLICT (account_id) DO UPDATE SET
        last_operation='credential.probe', last_error_code=NULL, last_error_message=NULL, updated_at=now()`, [accountID]);
    await audit(client as any, 'credential.probe', 'account', accountID, { status: 'succeeded' });
    return { account_id: accountID, account_name: account.rows[0].name, status: 'ok', error_code: null };
  } catch (error) {
    const code = error instanceof ApiError ? error.code : 'provider_operation_failed';
    const message = error instanceof Error ? error.message.replace(/[\x00-\x1f\x7f]/g, '').slice(0, 512) : String(error);
    await client.query(`INSERT INTO account_provider_state (account_id, last_operation, last_error_code, last_error_message, updated_at)
      VALUES ($1, 'credential.probe', $2, $3, now())
      ON CONFLICT (account_id) DO UPDATE SET
        last_operation='credential.probe', last_error_code=EXCLUDED.last_error_code,
        last_error_message=EXCLUDED.last_error_message, updated_at=now()`, [accountID, code, message]);
    await audit(client as any, 'credential.probe', 'account', accountID, { status: 'failed', error_code: code });
    return { account_id: accountID, account_name: account.rows[0].name, status: 'failed', error_code: code };
  }
}

export async function probeAllAccounts(client: Queryable, deps: ProbeDeps = {}): Promise<ProbeResult[]> {
  const accounts = await client.query('SELECT id, name FROM accounts WHERE enabled=true ORDER BY name');
  const results: ProbeResult[] = [];
  for (const account of accounts.rows) {
    results.push(await probeAccountCredentials(client, account.id, deps));
  }
  return results;
}
