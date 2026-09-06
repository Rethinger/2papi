// Compare a freshly measured optimization-mode matrix against a recorded
// BASELINE run, and flag drift.
//
// bench.mjs prints a table but does not persist JSON, so this takes the raw
// table, parses it, and diffs it against the baseline below.
//
//   docker compose --profile bench up -d --build fake-upstream gateway-bench
//   docker compose --profile bench run --rm --no-deps bench-matrix \
//     > test/results/matrix_raw.txt 2>&1
//   node test/matrix_compare.mjs test/results/matrix_raw.txt
//
// Writes test/results/gateway_matrix_report.json
//
// WHY THE COMPARISON IS RELATIVE, AND WHY IT IS GATED
//
// The first version of this script diffed absolute milliseconds against a
// snapshot taken on a different day. On 2026-09-04 that flagged 25 of 28 rows
// as "slower than documented" — including baseline (off) at +300%, a row that
// runs no optimizer at all. Nothing had regressed; the host was busier.
//
// So every mode is restated as its distance from the baseline row of the SAME
// run: ovh_delta = mode.ovh_avg - baseline.ovh_avg, plus rps_rel =
// mode.rps / baseline.rps for context. That removes the additive floor and
// makes baseline (off) structurally un-flaggable.
//
// It does NOT make milliseconds portable across hosts: most of an optimizer's
// overhead is CPU time, which scales with how busy the machine is, and
// subtracting a ~0.1 ms no-op row removes none of that. The 2026-09-04 run
// shows how far that goes — its huge/baseline row managed 38 rps where the
// same laptop does 101, and modes doing real work reported HIGHER throughput
// than the no-op row, which only load drift across a 14-mode sequence can
// produce.
//
// Hence the host-state gate. The baseline row's absolute throughput is used for
// exactly one purpose: deciding whether the run is comparable at all. Outside
// the band below, drift is suppressed with a reason instead of being published
// as 25 confident regressions.

import fs from 'fs';
import path from 'path';
import { pathToFileURL } from 'url';

// Recorded run this report diffs against. Refresh it only from a run whose
// squoze pin is proven (`go version -m` on the binary inside the bench image),
// and update every field of meta when you do.
export const BASELINE = {
  meta: {
    measured: '2026-09-06',
    squoze: 'v0.4.0',
    gateway_commit: 'ac098a4',
    verified_pin:
      'docker create + docker cp /app/gateway, then `go version -m gateway` → ' +
      'dep github.com/Rethinger/squoze v0.4.0',
    host:
      'AMD Ryzen 5 3550H, 8 logical CPUs and 10.7 GiB RAM visible inside the ' +
      'Docker VM, Windows 11 Pro + Docker Desktop (WSL2). A laptop, not an ' +
      'isolated bench host: treat absolute ms as a ceiling and rps as a floor.',
    harness:
      'fake upstream in the same compose network, conc=20, 6000 ms per mode, ' +
      '3 warmup requests per mode, 14 modes × 3 payload profiles',
    payloads: { small: '0.1 KiB', large: '96.9 KiB', huge: '633.4 KiB' },
    command:
      'docker compose --profile bench up -d --build fake-upstream gateway-bench && ' +
      'docker compose --profile bench run --rm --no-deps bench-matrix > test/results/matrix_raw.txt 2>&1 && ' +
      'node test/matrix_compare.mjs test/results/matrix_raw.txt',
  },
  profiles: {
    small: {
      'baseline (off)': { rps: 588, ovh_avg: 0.02 },
      'rtk light': { rps: 564, ovh_avg: 0.03 },
      'rtk standard': { rps: 640, ovh_avg: 0.03 },
      'rtk aggressive': { rps: 634, ovh_avg: 0.03 },
      'rtk auto': { rps: 711, ovh_avg: 0.02 },
      'caveman lite': { rps: 604, ovh_avg: 0.11 },
      'caveman full': { rps: 574, ovh_avg: 0.21 },
      'caveman auto': { rps: 610, ovh_avg: 0.13 },
      'headroom conservative': { rps: 599, ovh_avg: 0.03 },
      'headroom balanced': { rps: 592, ovh_avg: 0.03 },
      'headroom aggressive': { rps: 594, ovh_avg: 0.03 },
      'headroom auto': { rps: 534, ovh_avg: 0.05 },
      'all three (std/full/balanced)': { rps: 457, ovh_avg: 0.24 },
      'squoze (exclusive)': { rps: 498, ovh_avg: 0.05 },
    },
    large: {
      'baseline (off)': { rps: 307, ovh_avg: 0.08 },
      'rtk light': { rps: 227, ovh_avg: 16.75 },
      'rtk standard': { rps: 233, ovh_avg: 16.1 },
      'rtk aggressive': { rps: 219, ovh_avg: 16.85 },
      'rtk auto': { rps: 234, ovh_avg: 16.33 },
      'caveman lite': { rps: 218, ovh_avg: 19.4 },
      'caveman full': { rps: 207, ovh_avg: 20.32 },
      'caveman auto': { rps: 195, ovh_avg: 21.45 },
      'headroom conservative': { rps: 305, ovh_avg: 0.1 },
      'headroom balanced': { rps: 398, ovh_avg: 0.08 },
      'headroom aggressive': { rps: 351, ovh_avg: 0.09 },
      'headroom auto': { rps: 306, ovh_avg: 0.11 },
      'all three (std/full/balanced)': { rps: 222, ovh_avg: 22.93 },
      'squoze (exclusive)': { rps: 346, ovh_avg: 8.14 },
    },
    huge: {
      'baseline (off)': { rps: 101, ovh_avg: 0.3 },
      'rtk light': { rps: 41, ovh_avg: 185.15 },
      'rtk standard': { rps: 40, ovh_avg: 191.52 },
      'rtk aggressive': { rps: 50, ovh_avg: 138.07 },
      'rtk auto': { rps: 47, ovh_avg: 156.44 },
      'caveman lite': { rps: 43, ovh_avg: 204.3 },
      'caveman full': { rps: 46, ovh_avg: 187.81 },
      'caveman auto': { rps: 31, ovh_avg: 298.31 },
      'headroom conservative': { rps: 57, ovh_avg: 112.71 },
      'headroom balanced': { rps: 68, ovh_avg: 91.07 },
      'headroom aggressive': { rps: 74, ovh_avg: 75.81 },
      'headroom auto': { rps: 87, ovh_avg: 69.3 },
      'all three (std/full/balanced)': { rps: 73, ovh_avg: 83.46 },
      'squoze (exclusive)': { rps: 85, ovh_avg: 74.56 },
    },
  },
};

export const BASE_MODE = 'baseline (off)';

// A mode is flagged only when it drifts by both a share AND an absolute amount.
// The share alone would flag the small profile forever: 0.02 → 0.03 ms is +50%
// and is measurement noise, not a regression.
export const DRIFT_THRESHOLD_PCT = 20;
export const DRIFT_FLOOR_MS = 1.0;

// Host-state gate. measured baseline rps / recorded baseline rps must land in
// this band for per-mode millisecond drift to mean anything. The 2026-09-04 run
// scores 0.55 (large) and 0.38 (huge) and is correctly refused.
export const HOST_BAND = { min: 0.7, max: 1.4 };

export function parse(text) {
  const out = {};
  let profile = null;
  for (const line of text.split('\n')) {
    const head = line.match(/^###\s+payload=(\w+)/);
    if (head) {
      profile = head[1];
      out[profile] = {};
      continue;
    }
    if (!profile || !line.includes('|')) continue;
    const cols = line.split('|').map((c) => c.trim());
    if (cols.length < 8) continue;
    const [mode, reqs, rps, err, ovhAvg, ovhP95, , ttfbP95, applied] = cols;
    if (!mode || mode === 'mode' || mode.startsWith('---')) continue;
    if (!/^\d+$/.test(reqs)) continue;
    out[profile][mode] = {
      reqs: Number(reqs),
      rps: Number(rps),
      err: Number(err),
      ovh_avg: Number(ovhAvg),
      ovh_p95: Number(ovhP95),
      ttfb_p95: Number(ttfbP95),
      applied: applied || '',
    };
  }
  return out;
}

const round = (n, d = 2) => Number(n.toFixed(d));

// Restate a set of modes as distances from its own baseline row, which is what
// survives a move to a different host. Returns null when the run has no
// baseline row to normalise against — then only absolutes are reported.
export function normalize(modes) {
  const base = modes[BASE_MODE];
  if (!base) return null;
  const rel = {};
  for (const [mode, m] of Object.entries(modes)) {
    rel[mode] = {
      ovh_delta_ms: round(m.ovh_avg - base.ovh_avg),
      rps_rel: base.rps ? round(m.rps / base.rps, 3) : null,
    };
  }
  return rel;
}

export function driftPct(measured, baseline) {
  if (baseline === null || baseline === undefined) return null;
  if (baseline === 0) return measured === 0 ? 0 : null;
  return round(((measured - baseline) / Math.abs(baseline)) * 100, 1);
}

// Build the whole comparison report from a parsed run. Exported so the
// self-test can drive the host-state gate with synthetic runs instead of
// re-running the four-minute matrix.
export function compare(measured) {
  const report = {
    timestamp: new Date().toISOString(),
    suite: 'gateway optimization-mode matrix vs recorded baseline run',
    baseline: BASELINE.meta,
    comparison:
      'ovh_delta_ms (mode overhead minus the baseline row of the SAME run) and ' +
      'rps_rel (mode rps over the baseline row of the same run). Absolute ms ' +
      'and rps are recorded per row but are host-specific and not diffed.',
    drift_threshold_pct: DRIFT_THRESHOLD_PCT,
    drift_floor_ms: DRIFT_FLOOR_MS,
    host_band: HOST_BAND,
    host_state: {},
    profiles: {},
    significant_drift: [],
    throughput_drift: [],
    missing_from_baseline: [],
  };

  for (const [profile, modes] of Object.entries(measured)) {
    const bModes = BASELINE.profiles[profile];
    const mRel = normalize(modes);
    const bRel = bModes ? normalize(bModes) : null;
    if (!bModes) {
      report.profiles[profile] = { note: 'no recorded baseline for this profile', modes };
      continue;
    }
    if (!mRel) {
      report.profiles[profile] = { note: `measured run has no "${BASE_MODE}" row`, modes };
      continue;
    }

    // Host-state gate, decided before any per-mode claim is made.
    const bBase = bModes[BASE_MODE];
    const factor = bBase && bBase.rps ? round(modes[BASE_MODE].rps / bBase.rps, 3) : null;
    const comparable = factor !== null && factor >= HOST_BAND.min && factor <= HOST_BAND.max;
    report.host_state[profile] = {
      baseline_rps_recorded: bBase ? bBase.rps : null,
      baseline_rps_measured: modes[BASE_MODE].rps,
      host_factor: factor,
      verdict: comparable ? 'comparable' : factor === null ? 'unknown' : factor < HOST_BAND.min ? 'contended' : 'faster host',
      note: comparable
        ? null
        : `baseline row is ${factor}× the recorded run, outside ${HOST_BAND.min}–${HOST_BAND.max}: ` +
          'per-mode millisecond drift suppressed, the run is not comparable',
    };

    const rows = {};
    for (const [mode, m] of Object.entries(modes)) {
      const b = bModes[mode] || null;
      if (!b) report.missing_from_baseline.push({ profile, mode });
      const ovhDrift = b ? driftPct(mRel[mode].ovh_delta_ms, bRel[mode].ovh_delta_ms) : null;
      const rpsDrift = b ? driftPct(mRel[mode].rps_rel, bRel[mode].rps_rel) : null;
      rows[mode] = {
        measured: m,
        measured_rel: mRel[mode],
        baseline: b,
        baseline_rel: b ? bRel[mode] : null,
        ovh_delta_drift_pct: ovhDrift,
        rps_rel_drift_pct: rpsDrift,
      };
      if (!b || !comparable) continue;

      const gap = Math.abs(mRel[mode].ovh_delta_ms - bRel[mode].ovh_delta_ms);
      if (ovhDrift !== null && Math.abs(ovhDrift) >= DRIFT_THRESHOLD_PCT && gap >= DRIFT_FLOOR_MS) {
        report.significant_drift.push({
          profile,
          mode,
          baseline_ovh_delta_ms: bRel[mode].ovh_delta_ms,
          measured_ovh_delta_ms: mRel[mode].ovh_delta_ms,
          drift_pct: ovhDrift,
          direction: ovhDrift < 0 ? 'cheaper than baseline run' : 'costlier than baseline run',
        });
      }
      if (rpsDrift !== null && Math.abs(rpsDrift) >= DRIFT_THRESHOLD_PCT) {
        report.throughput_drift.push({
          profile,
          mode,
          baseline_rps_rel: bRel[mode].rps_rel,
          measured_rps_rel: mRel[mode].rps_rel,
          drift_pct: rpsDrift,
        });
      }
    }
    report.profiles[profile] = { modes: rows };
  }
  return report;
}

function main() {
  const src = process.argv[2];
  if (!src || !fs.existsSync(src)) {
    console.error('usage: node test/matrix_compare.mjs <bench-matrix-output.txt>');
    process.exit(2);
  }
  const measured = parse(fs.readFileSync(src, 'utf-8'));
  if (!Object.keys(measured).length) {
    console.error(`${src}: no "### payload=" sections found — did the run finish?`);
    process.exit(2);
  }

  const report = compare(measured);

  const pad = (s, n) => String(s).slice(0, n).padEnd(n);
  const rpad = (s, n) => String(s).padStart(n);
  const sign = (n) => (n > 0 ? '+' : '') + n;

  console.log('================================================================');
  console.log(` measured matrix vs baseline run ${BASELINE.meta.measured} (squoze ${BASELINE.meta.squoze})`);
  console.log('================================================================');
  console.log(' Δ = overhead above that run\'s own baseline row. Absolute ms is');
  console.log(' host-specific and shown only in the JSON report.');

  for (const [profile, data] of Object.entries(report.profiles)) {
    console.log(`\n### ${profile} (${BASELINE.meta.payloads[profile] ?? 'size unrecorded'})`);
    if (!data.modes || data.note) {
      console.log(`  (${data.note})`);
      continue;
    }
    const hs = report.host_state[profile];
    if (hs) {
      console.log(
        `  host: baseline ${hs.baseline_rps_measured} rps vs ${hs.baseline_rps_recorded} recorded ` +
          `(${hs.host_factor}×) -> ${hs.verdict}`
      );
      if (hs.note) console.log(`  ${hs.note}`);
    }
    console.log(
      `${pad('mode', 32)} ${rpad('base Δ', 9)} ${rpad('meas Δ', 9)} ${rpad('drift', 9)} ${rpad('base rel', 9)} ${rpad('meas rel', 9)}`
    );
    for (const [mode, r] of Object.entries(data.modes)) {
      console.log(
        `${pad(mode, 32)} ${rpad(r.baseline_rel ? r.baseline_rel.ovh_delta_ms : '—', 9)} ` +
          `${rpad(r.measured_rel.ovh_delta_ms, 9)} ` +
          `${rpad(r.ovh_delta_drift_pct === null ? '—' : sign(r.ovh_delta_drift_pct) + '%', 9)} ` +
          `${rpad(r.baseline_rel ? r.baseline_rel.rps_rel : '—', 9)} ${rpad(r.measured_rel.rps_rel, 9)}`
      );
    }
  }

  if (report.missing_from_baseline.length) {
    console.log('\n--- modes with no baseline row (record them or drop the mode) ---');
    for (const m of report.missing_from_baseline) console.log(`  ${m.profile}/${m.mode}`);
  }

  if (report.significant_drift.length) {
    console.log(`\n--- overhead drift ≥ ${DRIFT_THRESHOLD_PCT}% and ≥ ${DRIFT_FLOOR_MS}ms ---`);
    for (const d of report.significant_drift) {
      console.log(
        `  ${d.profile}/${d.mode}: baseline Δ${d.baseline_ovh_delta_ms}ms, measured Δ${d.measured_ovh_delta_ms}ms ` +
          `(${sign(d.drift_pct)}%, ${d.direction})`
      );
    }
  } else {
    const suppressed = Object.entries(report.host_state)
      .filter(([, h]) => h.verdict !== 'comparable')
      .map(([prof]) => prof);
    if (suppressed.length) {
      console.log(`\nNo drift claimed: ${suppressed.join(', ')} not comparable to the recorded run.`);
    } else {
      console.log(`\nNo overhead drift ≥ ${DRIFT_THRESHOLD_PCT}% and ≥ ${DRIFT_FLOOR_MS}ms.`);
    }
  }

  if (report.throughput_drift.length) {
    console.log(`\n--- relative throughput drift ≥ ${DRIFT_THRESHOLD_PCT}% (context, host-sensitive) ---`);
    for (const d of report.throughput_drift) {
      console.log(
        `  ${d.profile}/${d.mode}: baseline ${d.baseline_rps_rel}× baseline rps, measured ${d.measured_rps_rel}× (${sign(d.drift_pct)}%)`
      );
    }
  }

  fs.mkdirSync('test/results', { recursive: true });
  const out = path.join('test/results', 'gateway_matrix_report.json');
  fs.writeFileSync(out, JSON.stringify(report, null, 2), 'utf-8');
  console.log(`\nSaved ${out}`);
}

// Only run when invoked directly; the self-test imports the pieces above.
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main();
}
