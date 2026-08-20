import assert from 'node:assert/strict';
import test from 'node:test';
import { startDiscoveryScheduler, type DiscoveryRun, type DiscoverySchedulerHandle, type SchedulerDeps } from '../lib/discovery-scheduler.ts';

// Drain microtasks and pending setImmediate callbacks after advancing the
// fake clock; the scheduler's tick() completes asynchronously. Executor form:
// Promise.withResolvers requires lib es2024, the project targets es2022.
async function flush() {
  return new Promise<void>(resolve => { setImmediate(resolve); });
}

function runScheduler(t: test.TestContext, deps: SchedulerDeps) {
  t.mock.timers.enable({ apis: ['setTimeout', 'setInterval'] });
  const scheduler: DiscoverySchedulerHandle = startDiscoveryScheduler(deps);
  t.after(() => { scheduler.stop(); t.mock.timers.reset(); });
  return scheduler;
}

const emptyRun: DiscoveryRun = { scope: 'all', results: [] };

test('scheduler ticks after the initial delay, then on the interval', async t => {
  const calls: Array<{ scope: string }> = [];
  const discover = async (scope: { scope: 'all' }): Promise<DiscoveryRun> => {
    calls.push(scope);
    return emptyRun;
  };
  runScheduler(t, { intervalMs: 1000, initialDelayMs: 1000, discover, logger: () => {} });

  t.mock.timers.tick(999);
  await flush();
  assert.equal(calls.length, 0, 'must not fire before the initial delay');
  t.mock.timers.tick(1);
  await flush();
  assert.equal(calls.length, 1);
  assert.deepEqual(calls[0], { scope: 'all' });

  t.mock.timers.tick(1000);
  await flush();
  assert.equal(calls.length, 2);
  t.mock.timers.tick(1000);
  await flush();
  assert.equal(calls.length, 3);
});

test('overlapping runs are skipped and the schedule resumes', async t => {
  const starts: string[] = [];
  let release: () => void = () => {};
  const gate = new Promise<void>(resolve => { release = resolve; });
  let gated = false;
  const discover = async (): Promise<DiscoveryRun> => {
    starts.push('start');
    if (!gated) {
      gated = true;
      await gate;
    }
    return emptyRun;
  };
  runScheduler(t, { intervalMs: 1000, initialDelayMs: 0, discover, logger: () => {} });

  t.mock.timers.tick(1);
  await flush();
  assert.equal(starts.length, 1);

  t.mock.timers.tick(1000);
  await flush();
  assert.equal(starts.length, 1, 'interval tick during an in-flight run must be skipped');

  release();
  await flush();
  assert.equal(starts.length, 1, 'release alone must not start a run');

  t.mock.timers.tick(1000);
  await flush();
  assert.equal(starts.length, 2);
});

test('stop prevents further ticks', async t => {
  const calls: string[] = [];
  const discover = async (): Promise<DiscoveryRun> => {
    calls.push('tick');
    return emptyRun;
  };
  const scheduler = runScheduler(t, { intervalMs: 1000, initialDelayMs: 0, discover, logger: () => {} });

  t.mock.timers.tick(1);
  await flush();
  assert.equal(calls.length, 1);

  scheduler.stop();
  t.mock.timers.tick(5000);
  await flush();
  assert.equal(calls.length, 1);
});

test('failed run is logged and does not break the schedule', async t => {
  const attempts: string[] = [];
  const logged: string[] = [];
  const discover = async (): Promise<DiscoveryRun> => {
    attempts.push('attempt');
    throw new Error('upstream down');
  };
  runScheduler(t, { intervalMs: 1000, initialDelayMs: 0, discover, logger: message => { logged.push(message); } });

  t.mock.timers.tick(1);
  await flush();
  assert.equal(attempts.length, 1);
  assert.ok(logged.some(message => message.includes('auto-discovery failed')));

  t.mock.timers.tick(1000);
  await flush();
  assert.equal(attempts.length, 2, 'schedule must continue after a failed run');
});
