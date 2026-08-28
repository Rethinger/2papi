export async function register() {
  if (process.env.NEXT_RUNTIME !== 'nodejs') return;
  // Dynamic import is required here: Next bundles instrumentation for both
  // runtimes, and the DB pool (pg) must only load in the Node.js server.
  const { startDiscoveryScheduler } = await import('./lib/discovery-scheduler');
  startDiscoveryScheduler({
    intervalMs: Number(process.env.MODEL_DISCOVERY_INTERVAL_MS ?? 0) || undefined,
    initialDelayMs: Number(process.env.MODEL_DISCOVERY_INITIAL_DELAY_MS ?? 0) || undefined,
  });
  if (process.env.CREDENTIAL_PROBE_DISABLED !== '1') {
    const { startCredentialProber } = await import('./lib/credential-prober-scheduler');
    startCredentialProber({
      intervalMs: Number(process.env.CREDENTIAL_PROBE_INTERVAL_MS ?? 0) || undefined,
      initialDelayMs: Number(process.env.CREDENTIAL_PROBE_INITIAL_DELAY_MS ?? 0) || undefined,
    });
  }
  // Шаг 6 «Платежи»: nightly prepaid-balance reconcile (interval 0 disables).
  const { startBalanceReconciler } = await import('./lib/balance');
  const { pool } = await import('./lib/db');
  startBalanceReconciler(pool, {
    intervalMs: process.env.BALANCE_RECONCILE_INTERVAL_MS !== undefined ? Number(process.env.BALANCE_RECONCILE_INTERVAL_MS) : undefined,
  });
}
