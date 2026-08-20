import type { Pool, PoolClient } from 'pg';
import crypto from 'node:crypto';
import { ApiError } from '../api';
import { audit } from '../control';
import type { Queryable } from '../db';
import { accountUsageSince } from '../request-events';
import { dispatchProviderOperation, type OperationKind } from '../provider-operations';

type Dispatch = (
  client: Queryable,
  accountID: string,
  kind: OperationKind,
  input: unknown,
  idempotencyKey?: string,
) => Promise<{ data: unknown; warning_code?: string; credential_revision?: number }>;

type RefreshDeps = { dispatch?: Dispatch };

export type CodexResetOperation = {
  id: string;
  account_id: string;
  operation_type: 'quota_reset';
  idempotency_key: string;
  status: 'pending' | 'succeeded' | 'failed' | 'unknown';
  lease_expires_at: string | null;
  preflight: Record<string, unknown>;
  upstream_request_id: string;
  result_summary: Record<string, unknown>;
  warning_code: string | null;
  resolution_source: string | null;
  resolution_note: string | null;
  resolved_at: string | null;
  created_at: string;
  completed_at: string | null;
};

type ResetDeps = RefreshDeps & {
  randomUUID?: () => string;
  postRefresh?: boolean;
};

export async function getCodexQuota(client: Queryable, accountID: string) {
  const account = await client.query('SELECT id, name FROM accounts WHERE id=$1', [accountID]);
  if (!account.rows[0]) throw new ApiError(404, 'account_not_found', 'Account not found');
  await expirePendingReset(client, accountID);
  const state = await client.query(`SELECT account_id,quota,reset_credits,capability_status,fetched_at,last_operation,last_error_code,last_error_message,updated_at
    FROM account_provider_state WHERE account_id=$1`, [accountID]);
  const operation = await client.query(`SELECT * FROM provider_operations WHERE account_id=$1 AND operation_type='quota_reset'
    AND status IN ('pending','unknown') ORDER BY created_at DESC LIMIT 1`, [accountID]);
  const quota = state.rows[0] ?? {
    account_id: accountID,
    quota: {},
    reset_credits: {},
    capability_status: 'unknown',
    fetched_at: null,
    last_operation: null,
    last_error_code: null,
    last_error_message: null,
    updated_at: null,
  };
  const fetchedAt = quota.fetched_at ? new Date(quota.fetched_at) : null;
  if (fetchedAt && Number.isFinite(fetchedAt.getTime())) {
    const usage = await accountUsageSince(client, account.rows[0].name, fetchedAt);
    if (usage.requests > 0 || usage.tokens > 0) {
      return { ...quota, reset_operation: operation.rows[0] ?? null, local_usage: { ...usage, since: fetchedAt.toISOString() } };
    }
  }
  return { ...quota, reset_operation: operation.rows[0] ?? null, local_usage: null };
}

export async function refreshCodexQuota(client: Queryable, accountID: string, deps: RefreshDeps = {}) {
  const dispatch = deps.dispatch ?? dispatchProviderOperation;
  try {
    const usage = await dispatch(client, accountID, 'read_usage', {}, `quota-usage:${accountID}:${Date.now()}`);
    const resetCredits = await dispatch(client, accountID, 'list_reset_credits', {}, `quota-credits:${accountID}:${Date.now()}`);
    const quota = objectData(usage.data, 'codex_quota_contract_changed');
    const credits = objectData(resetCredits.data, 'codex_quota_contract_changed');
    return await inTransaction(client, async connection => {
      const stored = await connection.query(`INSERT INTO account_provider_state
        (account_id,quota,reset_credits,capability_status,fetched_at,last_operation,last_error_code,last_error_message,updated_at)
        VALUES ($1,$2,$3,'available',COALESCE(($2::jsonb->>'fetched_at')::timestamptz,now()),'quota.refresh',NULL,NULL,now())
        ON CONFLICT (account_id) DO UPDATE SET
          quota=EXCLUDED.quota,
          reset_credits=EXCLUDED.reset_credits,
          capability_status='available',
          fetched_at=EXCLUDED.fetched_at,
          last_operation='quota.refresh',
          last_error_code=NULL,
          last_error_message=NULL,
          updated_at=now()
        RETURNING *`, [accountID, JSON.stringify(quota), JSON.stringify(credits)]);
      await audit(connection, 'quota.refresh', 'account', accountID, {
        status: 'succeeded',
        warning_code: usage.warning_code ?? resetCredits.warning_code ?? null,
      });
      return stored.rows[0];
    });
  } catch (error) {
    const code = error instanceof ApiError ? error.code : 'provider_operation_failed';
    const capability = code === 'codex_quota_unsupported' ? 'unsupported' : code === 'codex_quota_contract_changed' ? 'contract_changed' : 'error';
    await inTransaction(client, async connection => {
      await connection.query(`INSERT INTO account_provider_state
        (account_id,capability_status,last_operation,last_error_code,last_error_message,updated_at)
        VALUES ($1,$2,'quota.refresh',$3,$4,now())
        ON CONFLICT (account_id) DO UPDATE SET
          capability_status=EXCLUDED.capability_status,
          last_operation='quota.refresh',
          last_error_code=EXCLUDED.last_error_code,
          last_error_message=EXCLUDED.last_error_message,
          updated_at=now()`, [accountID, capability, code, safeMessage(error)]);
      await audit(connection, 'quota.refresh', 'account', accountID, { status: 'failed', error_code: code });
    });
    throw error;
  }
}

export async function resetCodexQuota(
  client: Queryable,
  accountID: string,
  idempotencyKey: string,
  confirmed: boolean,
  deps: ResetDeps = {},
): Promise<CodexResetOperation> {
  if (!confirmed) throw new ApiError(400, 'quota_reset_confirmation_required', 'Reset confirmation is required');
  validateIdempotencyKey(idempotencyKey);
  const existing = await savedResetOperation(client, accountID, idempotencyKey);
  if (existing !== null) {
    return existing;
  }

  const dispatch = deps.dispatch ?? dispatchProviderOperation;
  const usageResult = await dispatch(client, accountID, 'read_usage', {}, `quota-preflight-usage:${idempotencyKey}`);
  const creditsResult = await dispatch(client, accountID, 'list_reset_credits', {}, `quota-preflight-credits:${idempotencyKey}`);
  const quota = objectData(usageResult.data, 'codex_quota_contract_changed');
  const resetCredits = objectData(creditsResult.data, 'codex_quota_contract_changed');
  requireAvailableCredit(resetCredits);
  const upstreamRequestID = (deps.randomUUID ?? crypto.randomUUID)();

  const prepared = await withAccountResetLock(client, accountID, async connection => {
    await expirePendingReset(connection, accountID);
    const same = await loadResetByKey(connection, accountID, idempotencyKey);
    if (same) return { operation: same, dispatch: false };
    const active = await connection.query(`SELECT * FROM provider_operations
      WHERE account_id=$1 AND operation_type='quota_reset' AND status IN ('pending','unknown')
      ORDER BY created_at DESC LIMIT 1`, [accountID]);
    if (active.rows[0]) throw new ApiError(409, 'quota_reset_active', 'A reset operation is pending or requires resolution', { operation_id: active.rows[0].id, status: active.rows[0].status });
    const account = await connection.query(`SELECT a.id FROM accounts a JOIN providers p ON p.id=a.provider_id
      WHERE a.id=$1 AND p.adapter='openai-codex'`, [accountID]);
    if (!account.rows[0]) throw new ApiError(404, 'codex_account_not_found', 'Codex account not found');
    const inserted = await connection.query(`INSERT INTO provider_operations
      (account_id,operation_type,idempotency_key,status,lease_expires_at,heartbeat_at,preflight,upstream_request_id,started_at)
      VALUES ($1,'quota_reset',$2,'pending',now()+interval '2 minutes',now(),$3,$4,now()) RETURNING *`,
    [accountID, idempotencyKey, JSON.stringify({ quota, reset_credits: resetCredits }), upstreamRequestID]);
    await audit(connection, 'quota.reset.start', 'provider_operation', inserted.rows[0].id, { account_id: accountID, status: 'pending' });
    return { operation: inserted.rows[0] as CodexResetOperation, dispatch: true };
  });
  if (!prepared.dispatch) {
    return prepared.operation;
  }

  try {
    const consumed = await dispatch(client, accountID, 'consume_reset_credit', { redeem_request_id: upstreamRequestID }, idempotencyKey);
    const completed = await updateResetAfterDispatch(client, prepared.operation.id, 'succeeded', {
      consumed: true,
      provider: objectData(consumed.data, 'provider_operation_invalid_response'),
    }, consumed.warning_code ?? null);
    if (deps.postRefresh !== false) {
      try { await refreshCodexQuota(client, accountID, { dispatch }); } catch { /* reset success is durable even when refresh fails */ }
    }
    return completed;
  } catch (error) {
    const status = conclusiveResetFailure(error) ? 'failed' : 'unknown';
    return updateResetAfterDispatch(client, prepared.operation.id, status, { error_code: safeErrorCode(error) }, status === 'unknown' ? 'quota_reset_outcome_unknown' : safeErrorCode(error));
  }
}

export async function reconcileCodexQuotaReset(
  client: Queryable,
  accountID: string,
  operationID: string,
  deps: RefreshDeps = {},
): Promise<CodexResetOperation> {
  const operation = await withAccountResetLock(client, accountID, async connection => {
    await expirePendingReset(connection, accountID);
    const found = await connection.query(`SELECT * FROM provider_operations
      WHERE id=$1 AND account_id=$2 AND operation_type='quota_reset' FOR UPDATE`, [operationID, accountID]);
    if (!found.rows[0]) throw new ApiError(404, 'quota_reset_operation_not_found', 'Reset operation not found');
    if (found.rows[0].status === 'succeeded' || found.rows[0].status === 'failed') return found.rows[0] as CodexResetOperation;
    if (found.rows[0].status === 'pending') throw new ApiError(409, 'quota_reset_pending', 'Reset operation is still pending');
    return found.rows[0] as CodexResetOperation;
  });
  if (operation.status !== 'unknown') return operation;

  const dispatch = deps.dispatch ?? dispatchProviderOperation;
  let quota: Record<string, unknown>;
  let resetCredits: Record<string, unknown>;
  try {
    quota = objectData((await dispatch(client, accountID, 'read_usage', {}, `quota-reconcile-usage:${operationID}`)).data, 'codex_quota_contract_changed');
    resetCredits = objectData((await dispatch(client, accountID, 'list_reset_credits', {}, `quota-reconcile-credits:${operationID}`)).data, 'codex_quota_contract_changed');
  } catch (error) {
    await client.query(`UPDATE provider_operations SET result_summary=$2,warning_code=$3 WHERE id=$1 AND status='unknown'`,
      [operationID, JSON.stringify({ reconciliation: 'inconclusive', error_code: safeErrorCode(error) }), safeErrorCode(error)]);
    return (await loadResetByID(client, accountID, operationID))!;
  }

  const conclusive = resetWasConsumed(operation.preflight, quota, resetCredits);
  return withAccountResetLock(client, accountID, async connection => {
    const status = conclusive ? 'succeeded' : 'unknown';
    const updated = await connection.query(`UPDATE provider_operations SET
      status=$3,result_summary=$4,resolution_source=CASE WHEN $3='succeeded' THEN 'reconciled' ELSE resolution_source END,
      resolved_at=CASE WHEN $3='succeeded' THEN now() ELSE resolved_at END,
      completed_at=CASE WHEN $3='succeeded' THEN now() ELSE completed_at END,
      lease_expires_at=NULL,heartbeat_at=now(),warning_code=CASE WHEN $3='unknown' THEN 'quota_reset_reconciliation_inconclusive' ELSE NULL END
      WHERE id=$1 AND account_id=$2 AND status='unknown' RETURNING *`,
    [operationID, accountID, status, JSON.stringify({ reconciliation: conclusive ? 'consumed' : 'inconclusive', quota, reset_credits: resetCredits })]);
    const row = (updated.rows[0] ?? (await loadResetByID(connection, accountID, operationID))) as CodexResetOperation;
    await audit(connection, 'quota.reset.reconcile', 'provider_operation', operationID, { account_id: accountID, status: row.status, conclusive });
    return row;
  });
}

export async function resolveCodexQuotaReset(
  client: Queryable,
  accountID: string,
  operationID: string,
  resolution: 'succeeded' | 'failed',
  note: string,
): Promise<CodexResetOperation> {
  const normalizedNote = note.trim();
  if (resolution !== 'succeeded' && resolution !== 'failed') throw new ApiError(400, 'resolution_invalid', 'Resolution must be succeeded or failed');
  if (normalizedNote.length < 10 || normalizedNote.length > 1000) throw new ApiError(400, 'resolution_note_required', 'A resolution note of 10 to 1000 characters is required');
  return withAccountResetLock(client, accountID, async connection => {
    await expirePendingReset(connection, accountID);
    const updated = await connection.query(`UPDATE provider_operations SET
      status=$3,resolution_source='manual',resolved_by='operator',resolution_note=$4,resolved_at=now(),completed_at=now(),lease_expires_at=NULL,
      result_summary=result_summary || $5::jsonb
      WHERE id=$1 AND account_id=$2 AND operation_type='quota_reset' AND status='unknown' RETURNING *`,
    [operationID, accountID, resolution, normalizedNote, JSON.stringify({ manual_resolution: resolution })]);
    if (!updated.rows[0]) {
      const existing = await loadResetByID(connection, accountID, operationID);
      if (!existing) throw new ApiError(404, 'quota_reset_operation_not_found', 'Reset operation not found');
      throw new ApiError(409, 'quota_reset_not_unknown', 'Only an unknown reset operation can be manually resolved');
    }
    await audit(connection, 'quota.reset.resolve', 'provider_operation', operationID, { account_id: accountID, resolution, note: normalizedNote });
    return updated.rows[0] as CodexResetOperation;
  });
}

function validateIdempotencyKey(key: string) {
  if (!key || key.length > 128 || /[\x00-\x20\x7f]/.test(key)) throw new ApiError(400, 'idempotency_key_invalid', 'A valid Idempotency-Key header is required');
}

function requireAvailableCredit(credits: Record<string, unknown>) {
  const available = Number(credits.available_count);
  const expiry = typeof credits.next_expires_at === 'string' ? Date.parse(credits.next_expires_at) : Number.POSITIVE_INFINITY;
  if (!Number.isInteger(available) || available <= 0 || !Number.isFinite(expiry) || expiry <= Date.now()) {
    throw new ApiError(409, 'quota_reset_credit_unavailable', 'No unexpired reset credit is available');
  }
}

async function savedResetOperation(client: Queryable, accountID: string, key: string) {
  await expirePendingReset(client, accountID, key);
  return loadResetByKey(client, accountID, key);
}

async function expirePendingReset(client: Queryable, accountID: string, key?: string) {
  await client.query(`UPDATE provider_operations SET status='unknown',warning_code='quota_reset_lease_expired',lease_expires_at=NULL,heartbeat_at=now()
    WHERE account_id=$1 AND operation_type='quota_reset' AND status='pending' AND lease_expires_at<=now()
      AND ($2::text IS NULL OR idempotency_key=$2)`, [accountID, key ?? null]);
}

async function loadResetByKey(client: Queryable, accountID: string, key: string): Promise<CodexResetOperation | null> {
  const result = await client.query(`SELECT * FROM provider_operations WHERE account_id=$1 AND operation_type='quota_reset' AND idempotency_key=$2`, [accountID, key]);
  return result.rows[0] ?? null;
}

async function loadResetByID(client: Queryable, accountID: string, id: string): Promise<CodexResetOperation | null> {
  const result = await client.query(`SELECT * FROM provider_operations WHERE id=$1 AND account_id=$2 AND operation_type='quota_reset'`, [id, accountID]);
  return result.rows[0] ?? null;
}

async function updateResetAfterDispatch(client: Queryable, id: string, status: 'succeeded' | 'failed' | 'unknown', summary: Record<string, unknown>, warning: string | null) {
  return inTransaction(client, async connection => {
    const result = await connection.query(`UPDATE provider_operations SET status=$2,result_summary=$3,warning_code=$4,
      lease_expires_at=NULL,heartbeat_at=now(),completed_at=CASE WHEN $2 IN ('succeeded','failed') THEN now() ELSE NULL END
      WHERE id=$1 AND status='pending' RETURNING *`, [id, status, JSON.stringify(summary), warning]);
    if (!result.rows[0]) throw new ApiError(409, 'quota_reset_state_conflict', 'Reset operation state changed unexpectedly');
    await audit(connection, 'quota.reset.finish', 'provider_operation', id, { status, warning_code: warning });
    return result.rows[0] as CodexResetOperation;
  });
}

async function withAccountResetLock<T>(client: Queryable, accountID: string, work: (connection: PoolClient) => Promise<T>) {
  return inTransaction(client, async connection => {
    await connection.query('SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0))', [accountID]);
    return work(connection);
  });
}

function conclusiveResetFailure(error: unknown) {
  if (!(error instanceof ApiError)) return false;
  return ['codex_reset_request_invalid', 'codex_reset_credit_failed', 'codex_quota_unsupported', 'credential_revision_conflict'].includes(error.code) && error.status < 500;
}

function safeErrorCode(error: unknown) {
  return error instanceof ApiError && /^[a-z0-9_]{1,128}$/.test(error.code) ? error.code : 'provider_operation_ambiguous';
}

function resetWasConsumed(preflight: Record<string, unknown>, quota: Record<string, unknown>, credits: Record<string, unknown>) {
  const priorCredits = objectDataOrEmpty(preflight.reset_credits);
  const beforeCount = Number(priorCredits.available_count);
  const afterCount = Number(credits.available_count);
  if (Number.isFinite(beforeCount) && Number.isFinite(afterCount) && afterCount < beforeCount) return true;
  const priorQuota = objectDataOrEmpty(preflight.quota);
  const beforeWindow = primaryWindow(priorQuota);
  const afterWindow = primaryWindow(quota);
  return beforeWindow.resetAt > 0 && afterWindow.resetAt > beforeWindow.resetAt && afterWindow.used < beforeWindow.used;
}

function objectDataOrEmpty(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {};
}

function primaryWindow(quota: Record<string, unknown>) {
  const limit = objectDataOrEmpty(quota.rate_limit);
  const window = objectDataOrEmpty(limit.primary_window);
  return { used: Number(window.used_percent) || 0, resetAt: Number(window.reset_at) || 0 };
}

function objectData(value: unknown, code: string): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new ApiError(502, code, 'Codex quota response contract changed');
  return value as Record<string, unknown>;
}

function safeMessage(error: unknown) {
  const message = error instanceof ApiError ? error.message : 'Provider operation failed';
  return message.replace(/[\x00-\x1f\x7f]/g, '').slice(0, 256);
}

async function inTransaction<T>(client: Queryable, work: (connection: PoolClient) => Promise<T>): Promise<T> {
  if (!isPool(client)) return work(client);
  const connection = await client.connect();
  try {
    await connection.query('BEGIN');
    const result = await work(connection);
    await connection.query('COMMIT');
    return result;
  } catch (error) {
    await connection.query('ROLLBACK');
    throw error;
  } finally {
    connection.release();
  }
}

function isPool(client: Queryable): client is Pool {
  return typeof (client as Pool).connect === 'function' && typeof (client as PoolClient).release !== 'function';
}
