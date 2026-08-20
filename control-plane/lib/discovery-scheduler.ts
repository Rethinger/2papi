import { discoverModelsForScope } from './codex/operations';
import { pool } from './db';

export type DiscoveryRun = { scope: string; results: Array<{ status: string }> };

export type SchedulerDeps = {
  intervalMs?: number;
  initialDelayMs?: number;
  discover?: (scope: { scope: 'all' }) => Promise<DiscoveryRun>;
  logger?: (message: string) => void;
};

export type DiscoverySchedulerHandle = {
  stop(): void;
};

export function startDiscoveryScheduler(deps: SchedulerDeps = {}): DiscoverySchedulerHandle {
  const intervalMs = deps.intervalMs ?? 6 * 60 * 60 * 1000;
  const initialDelayMs = deps.initialDelayMs ?? 60 * 1000;
  const discover = deps.discover ?? (() => discoverModelsForScope(pool, { scope: 'all' }));
  const logger = deps.logger ?? ((message: string) => { console.log(`[discovery-scheduler] ${message}`); });
  let timer: NodeJS.Timeout | null = null;
  let running = false;
  let stopped = false;

  async function tick() {
    if (running || stopped) return;
    running = true;
    const started = Date.now();
    try {
      const result = await discover({ scope: 'all' });
      const succeeded = result.results.filter(run => run.status === 'succeeded').length;
      logger(`auto-discovery: ${result.results.length} accounts, ${succeeded} ok, ${Date.now() - started}ms`);
    } catch (error) {
      logger(`auto-discovery failed: ${error instanceof Error ? error.message.replace(/[\x00-\x1f\x7f]/g, '').slice(0, 300) : String(error)}`);
    } finally {
      running = false;
    }
  }

  timer = setTimeout(() => {
    timer = setInterval(() => void tick(), intervalMs);
    void tick();
  }, initialDelayMs);
  // Never keep the process alive for the sake of an unref'd idle timer; the
  // Next server itself outlives this. Test runners and short-lived processes
  // exit cleanly.
  timer.unref?.();

  return {
    stop() {
      stopped = true;
      if (timer) {
        clearInterval(timer);
        timer = null;
      }
    },
  };
}
