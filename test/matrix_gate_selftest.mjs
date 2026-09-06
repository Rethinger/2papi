// Does the matrix comparator refuse to claim drift on a run it cannot compare?
//
// TSK-018 of .kiro/specs/benchmark-integrity replaced absolute-millisecond
// diffing with a host-state gate: per-mode drift is published only when the
// run's own "baseline (off)" row lands within HOST_BAND of the recorded run's.
// A passing matrix run cannot demonstrate that property -- a quiet host always
// answers "comparable" -- so the gate is driven here with synthetic runs built
// out of the recorded BASELINE itself, which also keeps this test in step with
// every future re-measurement.
//
//   node test/matrix_gate_selftest.mjs
import assert from 'assert';
import {
  BASELINE,
  BASE_MODE,
  HOST_BAND,
  DRIFT_THRESHOLD_PCT,
  compare,
} from './matrix_compare.mjs';

// A run shaped like parse() output. rpsFactor scales throughput (a contended
// host completes fewer requests) and ovhFactor scales overhead (the same host
// spends longer in the optimizer), so the pair models a host that changed
// speed rather than code that changed behaviour.
function replay({
  profiles = ['small', 'large', 'huge'],
  rpsFactor = 1,
  ovhFactor = 1,
  mutate = null,
} = {}) {
  const out = {};
  for (const profile of profiles) {
    out[profile] = {};
    for (const [mode, row] of Object.entries(BASELINE.profiles[profile])) {
      out[profile][mode] = {
        ...row,
        rps: Math.round(row.rps * rpsFactor),
        ovh_avg: Number((row.ovh_avg * ovhFactor).toFixed(2)),
      };
    }
    if (mutate) mutate(out[profile], profile);
  }
  return out;
}

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

console.log(`matrix host-state gate, band ${HOST_BAND.min}-${HOST_BAND.max}`);

check('replaying the recorded run is comparable and shows no drift', () => {
  const r = compare(replay());
  for (const profile of ['small', 'large', 'huge']) {
    assert.strictEqual(r.host_state[profile].verdict, 'comparable', profile);
    assert.strictEqual(r.host_state[profile].host_factor, 1, profile);
    assert.strictEqual(r.host_state[profile].note, null, profile);
  }
  assert.deepStrictEqual(r.significant_drift, []);
  assert.deepStrictEqual(r.throughput_drift, []);
  assert.deepStrictEqual(r.missing_from_baseline, []);
});

check('the host factor of the 2026-09-04 run is refused as contended', () => {
  // That run's huge/baseline row managed 38 rps where this baseline records
  // 101 on the same laptop, and its per-mode milliseconds were inflated with
  // it. Both facts are present here; neither may be reported as drift.
  const r = compare(replay({ profiles: ['huge'], rpsFactor: 38 / 101, ovhFactor: 2.5 }));
  const hs = r.host_state.huge;
  assert.strictEqual(hs.verdict, 'contended');
  assert.ok(hs.host_factor < HOST_BAND.min, `factor ${hs.host_factor} is inside the band`);
  assert.match(hs.note, /not comparable/);
  assert.deepStrictEqual(r.significant_drift, []);
  assert.deepStrictEqual(r.throughput_drift, []);
  // The drift is computed and kept per row for auditing, just never claimed.
  const row = r.profiles.huge.modes['caveman full'];
  assert.ok(
    row.ovh_delta_drift_pct > DRIFT_THRESHOLD_PCT,
    `expected a large per-row drift, got ${row.ovh_delta_drift_pct}`
  );
});

check('a host faster than the band is refused as well', () => {
  const r = compare(replay({ profiles: ['large'], rpsFactor: 2, ovhFactor: 0.3 }));
  assert.strictEqual(r.host_state.large.verdict, 'faster host');
  assert.ok(r.host_state.large.host_factor > HOST_BAND.max);
  assert.deepStrictEqual(r.significant_drift, []);
});

check('a real regression on a comparable host is flagged', () => {
  const r = compare(
    replay({
      profiles: ['huge'],
      mutate: (modes) => {
        modes['squoze (exclusive)'].ovh_avg += 40;
      },
    })
  );
  assert.strictEqual(r.host_state.huge.verdict, 'comparable');
  assert.strictEqual(r.significant_drift.length, 1);
  const hit = r.significant_drift[0];
  assert.strictEqual(hit.mode, 'squoze (exclusive)');
  assert.strictEqual(hit.direction, 'costlier than baseline run');
  assert.ok(hit.drift_pct >= DRIFT_THRESHOLD_PCT);
});

check('a throughput collapse in one mode is flagged', () => {
  const r = compare(
    replay({
      profiles: ['large'],
      mutate: (modes) => {
        modes['headroom balanced'].rps = Math.round(modes['headroom balanced'].rps / 2);
      },
    })
  );
  assert.strictEqual(r.host_state.large.verdict, 'comparable');
  assert.strictEqual(r.throughput_drift.length, 1);
  assert.strictEqual(r.throughput_drift[0].mode, 'headroom balanced');
});

check('millisecond noise under the floor is not a regression', () => {
  // small/rtk light moves 0.03 -> 0.05 ms. That is +200% of a 0.01 ms delta
  // and the reason the gate needs an absolute floor next to the percentage.
  const r = compare(
    replay({
      profiles: ['small'],
      mutate: (modes) => {
        modes['rtk light'].ovh_avg = 0.05;
      },
    })
  );
  assert.strictEqual(r.host_state.small.verdict, 'comparable');
  const row = r.profiles.small.modes['rtk light'];
  assert.ok(row.ovh_delta_drift_pct >= DRIFT_THRESHOLD_PCT, 'the percentage did fire');
  assert.deepStrictEqual(r.significant_drift, []);
});

check('a run without a baseline row makes no host claim', () => {
  const run = replay({ profiles: ['large'] });
  delete run.large[BASE_MODE];
  const r = compare(run);
  assert.strictEqual(r.host_state.large, undefined);
  assert.match(r.profiles.large.note, /has no/);
  assert.deepStrictEqual(r.significant_drift, []);
});

check('an unrecorded profile is passed through, not compared', () => {
  const r = compare({ tiny: { [BASE_MODE]: { rps: 10, ovh_avg: 0.1 } } });
  assert.match(r.profiles.tiny.note, /no recorded baseline/);
  assert.strictEqual(r.host_state.tiny, undefined);
});

check('a mode absent from the baseline is reported, not silently diffed', () => {
  const run = replay({ profiles: ['small'] });
  run.small['brand new mode'] = { rps: 500, ovh_avg: 0.04, err: 0 };
  const r = compare(run);
  assert.deepStrictEqual(r.missing_from_baseline, [{ profile: 'small', mode: 'brand new mode' }]);
  assert.strictEqual(r.profiles.small.modes['brand new mode'].baseline, null);
});

console.log(failures ? `${failures} check(s) failed` : 'all checks passed');
process.exit(failures ? 1 : 0);
