import type { Pool, PoolClient } from 'pg';
import { ApiError } from './api';
import { decryptSecretJson } from './crypto';
import type { Queryable } from './db';
import { env } from './env';

export type OperationKind = 'discover_models' | 'validate_credentials' | 'read_usage' | 'list_reset_credits' | 'consume_reset_credit';

const MAX_OPERATION_RESPONSE_BYTES = 2 * 1024 * 1024;

type RuntimeAccount = {
  id: string;
  name: string;
  adapter: string;
  base_url: string;
  credential: Record<string, unknown> & { revision: number };
  enabled: boolean;
  priority: number;
  weight: number;
  max_concurrency: number;
  cost: number;
};

type OperationResponse = {
  data: unknown;
  warning_code?: string;
  credential_revision?: number;
};

export async function dispatchProviderOperation(
  client: Queryable,
  accountID: string,
  kind: OperationKind,
  input: unknown,
  idempotencyKey = '',
): Promise<OperationResponse> {
  let runtimeAccount: RuntimeAccount | undefined;
  let body: string | undefined;
  try {
    runtimeAccount = await loadRuntimeAccount(client, accountID);
    body = JSON.stringify({ operation: kind, account: runtimeAccount, input: input ?? {}, idempotency_key: idempotencyKey });
    const response = await fetch(`${env.GATEWAY_INTERNAL_URL}/internal/v1/provider-operations`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${env.INTERNAL_SERVICE_TOKEN}`,
        'content-type': 'application/json',
      },
      body,
      signal: AbortSignal.timeout(operationTimeout(kind)),
    });
    const responseBody = await readResponseBounded(response, MAX_OPERATION_RESPONSE_BYTES);
    if (!response.ok) {
      throw new ApiError(
        response.status,
        response.status === 409 ? 'credential_revision_conflict' : 'provider_operation_failed',
        response.status === 409 ? 'Credential revision conflict' : 'Provider operation failed',
      );
    }
    if (responseBody.length === 0) return { data: {} };
    try {
      return JSON.parse(responseBody.toString('utf8')) as OperationResponse;
    } catch {
      throw new ApiError(502, 'provider_operation_invalid_response', 'Provider operation returned invalid JSON');
    }
  } finally {
    body = undefined;
    runtimeAccount = undefined;
  }
}

async function loadRuntimeAccount(client: Queryable, accountID: string): Promise<RuntimeAccount> {
  const pool = isPool(client) ? client : null;
  const connection = pool ? await pool.connect() : client;
  try {
    if (pool) await connection.query('BEGIN');
    const result = await connection.query(`SELECT a.*, p.adapter,
      sr.key_version, sr.data_key_nonce, sr.data_key_ciphertext, sr.data_key_tag,
      sr.secret_nonce, sr.secret_ciphertext, sr.secret_tag
      FROM accounts a
      JOIN providers p ON p.id=a.provider_id
      JOIN secret_records sr ON sr.id=a.secret_record_id
      WHERE a.id=$1`, [accountID]);
    const row = result.rows[0];
    if (!row) throw new ApiError(404, 'account_not_found', 'Account not found');
    const credential = decryptSecretJson<Record<string, unknown>>({
      key_version: row.key_version,
      data_key_nonce: row.data_key_nonce.toString('base64'),
      data_key_ciphertext: row.data_key_ciphertext.toString('base64'),
      data_key_tag: row.data_key_tag.toString('base64'),
      secret_nonce: row.secret_nonce.toString('base64'),
      secret_ciphertext: row.secret_ciphertext.toString('base64'),
      secret_tag: row.secret_tag.toString('base64'),
    });
    const account: RuntimeAccount = {
      id: row.id,
      name: row.name,
      adapter: row.adapter,
      base_url: row.base_url,
      credential: { ...credential, revision: Number(row.credential_revision) },
      enabled: row.enabled,
      priority: row.priority,
      weight: row.weight,
      max_concurrency: row.max_concurrency,
      cost: Number(row.cost),
    };
    if (pool) await connection.query('COMMIT');
    return account;
  } catch (error) {
    if (pool) await connection.query('ROLLBACK');
    throw error;
  } finally {
    if (pool) (connection as PoolClient).release();
  }
}

function isPool(client: Queryable): client is Pool {
  return typeof (client as Pool).connect === 'function';
}

function operationTimeout(kind: OperationKind): number {
  switch (kind) {
    case 'discover_models':
    case 'consume_reset_credit':
      return 30_000;
    default:
      return 20_000;
  }
}

async function readResponseBounded(response: Response, maxBytes: number): Promise<Buffer> {
  if (!response.body) return Buffer.alloc(0);
  const reader = response.body.getReader();
  const chunks: Buffer[] = [];
  let size = 0;
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      size += value.byteLength;
      if (size > maxBytes) {
        await reader.cancel();
        throw new ApiError(502, 'provider_operation_response_too_large', 'Provider operation response exceeded the size limit');
      }
      chunks.push(Buffer.from(value));
    }
  } finally {
    reader.releaseLock();
  }
  return Buffer.concat(chunks, size);
}
