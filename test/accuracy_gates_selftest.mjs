// Does the accuracy runner judge, or does it only print?
//
// AC-11.2 of .kiro/specs/benchmark-integrity requires the suite's gates
// (delta accuracy <= 2 pp, savings >= 50%, overhead p95 <= 15 ms) to come out as
// pass/fail rather than as text next to a threshold. That property has to hold
// when no provider is reachable, which is exactly when the live suite cannot run
// -- so the judging is exported and exercised here on synthetic reports.
//
//   node test/accuracy_gates_selftest.mjs
import assert from 'assert';
import { evaluateGates } from './accuracy_suite.mjs';

const GATES = { delta_accuracy_pp_max: 2, savings_pct_min: 50, overhead_p95_ms_max: 15 };

// A fixture entry shaped like the real one, with only the fields the gates read.
function fixture({ fired = 3, deltaPp = 0, savedPct = 90, latencies = [4, 5, 6], inTok = 1000 } = {}) {
  return {
    fixture: 'synthetic.json',
    delta_score_pp: deltaPp,
    squoze: { squoze_active_runs: fired, saved_pct_median: savedPct, prompt_tokens_median: inTok },
    baseline: { prompt_tokens_median: inTok },
    trials: {
      squoze: latencies.map((ms) => ({ squozeActive: ms > 0, squozeLatencyMs: ms })),
      baseline: [],
    },
  };
}

const run = (fixtures) => evaluateGates({ gates: { ...GATES }, fixtures });

let failures = 0;
function check(name, fn) {
  try {
    fn();
    console.log(`  ok    ${name}`);
  } catch (err) {
    failures += 1;
    console.log(`  FAIL  ${name}: ${err.message}`);
  }
}

console.log('accuracy gate self-test');

check('all thresholds met -> PASS', () => {
  const r = run([fixture(), fixture({ deltaPp: 1.5, savedPct: 62 })]);
  assert.strictEqual(r.status, 'PASS');
  assert.strictEqual(r.gate_results.delta_accuracy.verdict, 'pass');
  assert.strictEqual(r.gate_results.savings.verdict, 'pass');
  assert.strictEqual(r.gate_results.overhead.verdict, 'pass');
});

check('squoze never fired -> INCONCLUSIVE, not PASS', () => {
  const r = run([fixture({ fired: 0, latencies: [0, 0, 0] })]);
  assert.strictEqual(r.status, 'INCONCLUSIVE');
  assert.match(r.inconclusive_reason, /did not fire/);
  assert.strictEqual(r.gate_results.fixtures_where_squoze_fired, 0);
});

check('accuracy drop beyond 2 pp -> FAIL', () => {
  const r = run([fixture(), fixture({ deltaPp: -3.1 })]);
  assert.strictEqual(r.status, 'FAIL');
  assert.strictEqual(r.gate_results.delta_accuracy.verdict, 'fail');
  assert.strictEqual(r.gate_results.delta_accuracy.worst_pp, -3.1);
});

check('accuracy gain is never a failure', () => {
  const r = run([fixture({ deltaPp: 7 })]);
  assert.strictEqual(r.gate_results.delta_accuracy.verdict, 'pass');
});

check('savings under 50% -> FAIL', () => {
  const r = run([fixture({ savedPct: 41 }), fixture({ savedPct: 44 })]);
  assert.strictEqual(r.status, 'FAIL');
  assert.strictEqual(r.gate_results.savings.verdict, 'fail');
});

check('overhead p95 over 15 ms -> FAIL', () => {
  const r = run([fixture({ latencies: [4, 5, 40, 38, 41] })]);
  assert.strictEqual(r.status, 'FAIL');
  assert.strictEqual(r.gate_results.overhead.verdict, 'fail');
});

// The regression this guards: a request squoze skipped reports latency 0. Counting
// those in the p95 measures the overhead of not compressing, which is always fine,
// so a real overhead problem would pass the gate.
check('skipped trials do not dilute the overhead p95', () => {
  const fired = [30, 31, 32];
  const withSkips = { ...fixture({ latencies: fired }) };
  withSkips.trials.squoze.push(...[0, 0, 0, 0, 0, 0, 0, 0, 0].map((ms) => ({ squozeActive: false, squozeLatencyMs: ms })));
  const r = evaluateGates({ gates: { ...GATES }, fixtures: [withSkips] });
  assert.strictEqual(r.gate_results.overhead.verdict, 'fail', 'zeros from skipped trials were averaged in');
  assert.ok(r.gate_results.overhead.squoze_p95_ms >= 30, `p95 = ${r.gate_results.overhead.squoze_p95_ms}`);
});

check('errored trials are excluded from the overhead p95', () => {
  const f = fixture({ latencies: [5, 6, 7] });
  f.trials.squoze.push({ error: 'HTTP 502', squozeActive: true, squozeLatencyMs: 9999 });
  const r = evaluateGates({ gates: { ...GATES }, fixtures: [f] });
  assert.strictEqual(r.gate_results.overhead.verdict, 'pass');
  assert.ok(r.gate_results.overhead.squoze_p95_ms < 15);
});

check('constant prompt_tokens is reported as unusable for savings', () => {
  const r = run([fixture({ inTok: 1016 }), fixture({ inTok: 1016 })]);
  assert.strictEqual(r.provider_usage.usable_for_savings, false);
  assert.strictEqual(r.provider_usage.distinct_prompt_tokens_values, 1);
  assert.match(r.provider_usage.note, /placeholder/);
});

check('prompt_tokens that tracks payload size is usable', () => {
  const r = run([fixture({ inTok: 400 }), fixture({ inTok: 9000 })]);
  assert.strictEqual(r.provider_usage.usable_for_savings, true);
  assert.match(r.provider_usage.note, /is a measurement/);
});

check('an unjudgeable gate yields PARTIAL, never PASS', () => {
  const f = fixture({ deltaPp: null });
  const r = evaluateGates({ gates: { ...GATES }, fixtures: [f] });
  assert.strictEqual(r.gate_results.delta_accuracy.verdict, null);
  assert.strictEqual(r.status, 'PARTIAL');
});

console.log(failures ? `${failures} check(s) failed` : 'all checks passed');
process.exit(failures ? 1 : 0);
