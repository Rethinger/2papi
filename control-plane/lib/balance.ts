import type { Pool } from 'pg';

// Шаг 6 «Платежи»: the ledger is the source of truth for prepaid balances.
// Nightly reconcile recomputes every tracked team's balance as
//   SUM(credit_transactions.delta_usd) − Σ successful request_events spend
// and logs any drift against the live value (which the gateway sees through
// snapshot freshness). Divergence is an alert, not an error.

export async function reconcileTeamBalances(db: Pool): Promise<{ updated: number; drifted: number }> {
  const client = await db.connect();
  let updated = 0;
  let drifted = 0;
  try {
    await client.query('BEGIN');
    const rows = (await client.query(`
      SELECT t.id,
             COALESCE((SELECT sum(ct.delta_usd) FROM credit_transactions ct WHERE ct.team_id = t.id), 0)
               - COALESCE((
                   SELECT sum(re.cost_usd) FROM request_events re
                   JOIN virtual_keys vk ON vk.id = re.virtual_key_id
                   WHERE vk.team_id = t.id AND re.success
                 ), 0) AS computed,
             t.balance_usd AS live
      FROM teams t
      FOR UPDATE`)).rows;
    for (const row of rows) {
      const computed = Number(row.computed);
      const live = Number(row.live ?? 0);
      if (Math.abs(computed - live) > 1e-6) {
        drifted += 1;
        console.error(`[balance-reconcile] team ${row.id} drifted: live=${live} computed=${computed}`);
      }
      await client.query('UPDATE teams SET balance_usd=$2, updated_at=now() WHERE id=$1', [row.id, Math.max(computed, 0)]);
      updated += 1;
    }
    await client.query('COMMIT');
  } catch (error) {
    await client.query('ROLLBACK');
    throw error;
  } finally {
    client.release();
  }
  return { updated, drifted };
}

// Scheduler wiring for instrumentation.ts. Disabled with interval 0.
export function startBalanceReconciler(db: Pool, options?: { intervalMs?: number }) {
  const intervalMs = options?.intervalMs
    ?? (process.env.BALANCE_RECONCILE_INTERVAL_MS !== undefined ? Number(process.env.BALANCE_RECONCILE_INTERVAL_MS) : 24 * 3600_000);
  if (!Number.isFinite(intervalMs) || intervalMs <= 0) {
    return { stop() { /* disabled */ } };
  }
  let stopped = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const run = async () => {
    while (!stopped) {
      try {
        await reconcileTeamBalances(db);
      } catch (error) {
        console.error('[balance-reconcile] failed', error);
      }
      if (stopped) break;
      timer = setTimeout(run, intervalMs);
      return;
    }
  };
  void run();
  return {
    stop() {
      stopped = true;
      if (timer) clearTimeout(timer);
    },
  };
}
