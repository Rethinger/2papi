import { probeAllAccounts, type ProbeResult } from './credential-prober';
import { pool } from './db';

export type CredentialProberDeps = {
  intervalMs?: number;
  initialDelayMs?: number;
  probe?: (client: typeof pool) => Promise<ProbeResult[]>;
  logger?: (message: string) => void;
};

export type CredentialProberHandle = {
  stop(): void;
};

export function startCredentialProber(deps: CredentialProberDeps = {}): CredentialProberHandle {
  const intervalMs = deps.intervalMs ?? 6 * 60 * 60 * 1000;
  const initialDelayMs = deps.initialDelayMs ?? 90 * 1000;
  const probe = deps.probe ?? (() => probeAllAccounts(pool));
  const logger = deps.logger ?? ((message: string) => { console.log(`[credential-prober] ${message}`); });
  let timer: NodeJS.Timeout | null = null;
  let running = false;
  let stopped = false;

  async function tick() {
    if (running || stopped) return;
    running = true;
    const started = Date.now();
    try {
      const results = await probe(pool);
      const failed = results.filter(result => result.status === 'failed').length;
      logger(`credential probe: ${results.length} accounts, ${failed} failed, ${Date.now() - started}ms`);
    } catch (error) {
      logger(`credential probe failed: ${error instanceof Error ? error.message.replace(/[\x00-\x1f\x7f]/g, '').slice(0, 300) : String(error)}`);
    } finally {
      running = false;
    }
  }

  timer = setTimeout(() => {
    timer = setInterval(() => void tick(), intervalMs);
    void tick();
  }, initialDelayMs);
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
