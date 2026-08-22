import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import { Pool } from 'pg';
import { ApiError } from '../lib/api.ts';
import { getCodexQuota, reconcileCodexQuotaReset, refreshCodexQuota, resetCodexQuota, resolveCodexQuotaReset } from '../lib/codex/quota.ts';

const url = process.env.TEST_DATABASE_URL;
const options = { skip: url ? false : 'TEST_DATABASE_URL is not set' };
const pool = url ? new Pool({ connectionString: url, max: 2 }) : null;

test.before(async () => {
  if (!pool) return;
  await pool.query('DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;');
  const dir = path.join(process.cwd(), 'migrations');
  for (const name of (await fs.readdir(dir)).filter(name => name.endsWith('.sql')).sort()) {
    await pool.query(await fs.readFile(path.join(dir, name), 'utf8'));
  }
});

test.after(async () => { await pool?.end(); });

let accountSequence = 0;

async function seedAccount() {
  accountSequence += 1;
  const provider = (await pool!.query("SELECT id FROM providers WHERE slug='openai-codex'")).rows[0];
  return (await pool!.query(
    "INSERT INTO accounts (provider_id,name,display_name,base_url) VALUES ($1,$2,$3,'https://chatgpt.com/backend-api/codex') RETURNING id",
    [provider.id, `quota-it-${accountSequence}`, `Quota IT ${accountSequence}`],
  )).rows[0].id as string;
}

test('quota refresh persists normalized usage and reset-credit state', options, async () => {
  const accountID = await seedAccount();
  const kinds: string[] = [];
  const state = await refreshCodexQuota(pool!, accountID, {
    dispatch: async (_client, _accountID, kind) => {
      kinds.push(kind);
      if (kind === 'read_usage') return { data: { plan_type: 'plus', rate_limit: { primary_window: { used_percent: 42, reset_at: 1787122967 } }, fetched_at: '2026-08-12T12:00:00Z' } };
      return { data: { available_count: 1, total_earned_count: 3, next_expires_at: new Date(Date.now() + 30 * 864e5).toISOString(), fetched_at: '2026-08-12T12:00:00Z' } };
    },
  });

  assert.deepEqual(kinds, ['read_usage', 'list_reset_credits']);
  assert.equal(state.capability_status, 'available');
  assert.equal(state.quota.rate_limit.primary_window.used_percent, 42);
  assert.equal(state.reset_credits.available_count, 1);
  const loaded = await getCodexQuota(pool!, accountID);
  assert.equal(loaded.quota.plan_type, 'plus');
  assert.equal((await pool!.query("SELECT count(*)::int n FROM audit_events WHERE action='quota.refresh' AND resource_id=$1", [accountID])).rows[0].n, 1);
});

test('failed refresh preserves last successful quota and records capability error', options, async () => {
  const accountID = await seedAccount();
  await pool!.query(`INSERT INTO account_provider_state (account_id,quota,reset_credits,capability_status,fetched_at)
    VALUES ($1,$2,$3,'available',now())`, [accountID, JSON.stringify({ plan_type: 'plus', marker: 'keep' }), JSON.stringify({ available_count: 0 })]);

  await assert.rejects(
    refreshCodexQuota(pool!, accountID, { dispatch: async () => { throw new ApiError(409, 'codex_quota_unsupported', 'unsupported'); } }),
    (error: any) => error.code === 'codex_quota_unsupported',
  );
  const loaded = await getCodexQuota(pool!, accountID);
  assert.equal(loaded.quota.marker, 'keep');
  assert.equal(loaded.capability_status, 'unsupported');
  assert.equal(loaded.last_error_code, 'codex_quota_unsupported');
});

const preflightUsage = { plan_type: 'plus', rate_limit: { primary_window: { used_percent: 100, reset_at: 1787122967 } }, fetched_at: '2026-08-12T12:00:00Z' };
const preflightCredits = { available_count: 1, total_earned_count: 3, next_expires_at: new Date(Date.now() + 30 * 864e5).toISOString(), fetched_at: '2026-08-12T12:00:00Z' };

test('quota reset stores pending before one dispatch and reuses idempotency result', options, async () => {
  const accountID = await seedAccount();
  const dispatched: string[] = [];
  const dispatch = async (_client: any, _accountID: string, kind: any, input: any) => {
    if (kind === 'read_usage') return { data: preflightUsage };
    if (kind === 'list_reset_credits') return { data: preflightCredits };
    const pending = await pool!.query("SELECT status,upstream_request_id FROM provider_operations WHERE account_id=$1 AND operation_type='quota_reset'", [accountID]);
    assert.equal(pending.rows[0].status, 'pending');
    assert.equal(input.redeem_request_id, pending.rows[0].upstream_request_id);
    dispatched.push(input.redeem_request_id);
    return { data: { consumed: true } };
  };
  const key = 'reset-browser-1';
  const first = await resetCodexQuota(pool!, accountID, key, true, { dispatch, randomUUID: () => '66d28bee-2f55-41fc-aba8-b8ef8b07a923', postRefresh: false });
  const retry = await resetCodexQuota(pool!, accountID, key, true, { dispatch, randomUUID: () => 'different-uuid', postRefresh: false });
  assert.equal(first.status, 'succeeded');
  assert.equal(retry.id, first.id);
  assert.equal(retry.status, 'succeeded');
  assert.deepEqual(dispatched, ['66d28bee-2f55-41fc-aba8-b8ef8b07a923']);
});

test('active and ambiguous resets block later spends until audited resolution', options, async () => {
  const accountID = await seedAccount();
  const dispatch = async (_client: any, _accountID: string, kind: any) => {
    if (kind === 'read_usage') return { data: preflightUsage };
    if (kind === 'list_reset_credits') return { data: preflightCredits };
    throw new TypeError('connection closed after write');
  };
  const unknown = await resetCodexQuota(pool!, accountID, 'ambiguous-1', true, { dispatch, randomUUID: () => '66d28bee-2f55-41fc-aba8-b8ef8b07a924', postRefresh: false });
  assert.equal(unknown.status, 'unknown');
  await assert.rejects(
    resetCodexQuota(pool!, accountID, 'different-key', true, { dispatch, randomUUID: () => '66d28bee-2f55-41fc-aba8-b8ef8b07a925', postRefresh: false }),
    (error: any) => error.code === 'quota_reset_active',
  );
  await assert.rejects(
    resolveCodexQuotaReset(pool!, accountID, unknown.id, 'failed', 'no'),
    (error: any) => error.code === 'resolution_note_required',
  );
  const resolved = await resolveCodexQuotaReset(pool!, accountID, unknown.id, 'failed', 'Verified in the upstream usage page: no reset occurred');
  assert.equal(resolved.status, 'failed');
  assert.equal(resolved.resolution_source, 'manual');
  assert.equal((await pool!.query("SELECT count(*)::int n FROM audit_events WHERE action='quota.reset.resolve' AND resource_id=$1", [unknown.id])).rows[0].n, 1);
});

test('reconciliation succeeds only on conclusive credit spend evidence', options, async () => {
  const accountID = await seedAccount();
  const operation = (await pool!.query(`INSERT INTO provider_operations
    (account_id,operation_type,idempotency_key,status,preflight,upstream_request_id,started_at)
    VALUES ($1,'quota_reset','reconcile-1','unknown',$2,$3,now()) RETURNING *`,
    [accountID, JSON.stringify({ quota: preflightUsage, reset_credits: preflightCredits }), '66d28bee-2f55-41fc-aba8-b8ef8b07a926'])).rows[0];
  const inconclusive = await reconcileCodexQuotaReset(pool!, accountID, operation.id, {
    dispatch: async (_client, _account, kind) => ({ data: kind === 'read_usage' ? preflightUsage : preflightCredits }),
  });
  assert.equal(inconclusive.status, 'unknown');
  const conclusive = await reconcileCodexQuotaReset(pool!, accountID, operation.id, {
    dispatch: async (_client, _account, kind) => ({ data: kind === 'read_usage' ? { ...preflightUsage, rate_limit: { primary_window: { used_percent: 0, reset_at: 1787722967 } } } : { ...preflightCredits, available_count: 0 } }),
  });
  assert.equal(conclusive.status, 'succeeded');
  assert.equal(conclusive.resolution_source, 'reconciled');
});

test('quota reset requires confirmation and available unexpired credit', options, async () => {
  const accountID = await seedAccount();
  await assert.rejects(resetCodexQuota(pool!, accountID, 'no-confirm', false), (error: any) => error.code === 'quota_reset_confirmation_required');
  await assert.rejects(
    resetCodexQuota(pool!, accountID, 'no-credit', true, {
      dispatch: async (_client, _account, kind) => ({ data: kind === 'read_usage' ? preflightUsage : { ...preflightCredits, available_count: 0 } }),
      randomUUID: () => '66d28bee-2f55-41fc-aba8-b8ef8b07a927', postRefresh: false,
    }),
    (error: any) => error.code === 'quota_reset_credit_unavailable',
  );
});
