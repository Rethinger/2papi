// Compare a freshly measured optimization-mode matrix against the numbers
// published in docs/benchmarks.md, and flag drift.
//
// bench.mjs prints a table but does not persist JSON, so this takes the raw
// table on stdin, parses it, and diffs it against the documented values.
//
//   docker compose --profile bench run --rm bench-matrix > /tmp/matrix.txt
//   node test/matrix_compare.mjs /tmp/matrix.txt
//
// Writes test/results/gateway_matrix_report.json

import fs from 'fs';
import path from 'path';

// Values as published in docs/benchmarks.md (measured 2026-09-02).
const DOCUMENTED = {
  large: {
    'baseline (off)': { rps: 336, ovh_avg: 0.09 },
    'rtk light': { rps: 272, ovh_avg: 12.5 },
    'rtk standard': { rps: 264, ovh_avg: 13.02 },
    'rtk aggressive': { rps: 294, ovh_avg: 11.76 },
    'rtk auto': { rps: 270, ovh_avg: 12.76 },
    'caveman lite': { rps: 262, ovh_avg: 15.18 },
    'caveman full': { rps: 286, ovh_avg: 14.19 },
    'headroom conservative': { rps: 372, ovh_avg: 0.08 },
    'headroom balanced': { rps: 356, ovh_avg: 0.09 },
    'headroom aggressive': { rps: 372, ovh_avg: 0.08 },
    'headroom auto': { rps: 363, ovh_avg: 0.09 },
    'all three (std/full/balanced)': { rps: 231, ovh_avg: 20.66 },
    'squoze (exclusive)': { rps: 287, ovh_avg: 12.0 },
  },
  huge: {
    'baseline (off)': { rps: 97, ovh_avg: 0.11 },
    'rtk light': { rps: 61, ovh_avg: 112.31 },
    'rtk standard': { rps: 66, ovh_avg: 98.42 },
    'rtk aggressive': { rps: 64, ovh_avg: 109.81 },
    'rtk auto': { rps: 63, ovh_avg: 112.0 },
    'caveman lite': { rps: 60, ovh_avg: 134.02 },
    'caveman full': { rps: 63, ovh_avg: 125.09 },
    'headroom conservative': { rps: 114, ovh_avg: 51.16 },
    'headroom balanced': { rps: 123, ovh_avg: 48.02 },
    'headroom aggressive': { rps: 128, ovh_avg: 43.21 },
    'headroom auto': { rps: 127, ovh_avg: 44.14 },
    'all three (std/full/balanced)': { rps: 118, ovh_avg: 48.18 },
    'squoze (exclusive)': { rps: 31, ovh_avg: 438.56 },
  },
};

const DRIFT_THRESHOLD_PCT = 20;

function parse(text) {
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

function driftPct(measured, documented) {
  if (!documented) return null;
  if (documented === 0) return measured === 0 ? 0 : Infinity;
  return Number((((measured - documented) / documented) * 100).toFixed(1));
}

function main() {
  const src = process.argv[2];
  if (!src || !fs.existsSync(src)) {
    console.error('usage: node test/matrix_compare.mjs <bench-matrix-output.txt>');
    process.exit(2);
  }
  const measured = parse(fs.readFileSync(src, 'utf-8'));

  const report = {
    timestamp: new Date().toISOString(),
    suite: 'gateway optimization-mode matrix vs docs/benchmarks.md',
    documented_source: 'docs/benchmarks.md, measured 2026-09-02',
    drift_threshold_pct: DRIFT_THRESHOLD_PCT,
    note:
      'Absolute throughput depends on host load, so rps drift is expected and is ' +
      'reported for context. The actionable signal is overhead drift on a mode ' +
      'whose implementation changed.',
    profiles: {},
    significant_drift: [],
  };

  for (const [profile, modes] of Object.entries(measured)) {
    const doc = DOCUMENTED[profile];
    if (!doc) {
      report.profiles[profile] = { note: 'no documented baseline for this profile', modes };
      continue;
    }
    const rows = {};
    for (const [mode, m] of Object.entries(modes)) {
      const d = doc[mode];
      const ovhDrift = driftPct(m.ovh_avg, d?.ovh_avg);
      const rpsDrift = driftPct(m.rps, d?.rps);
      rows[mode] = {
        measured: m,
        documented: d || null,
        ovh_drift_pct: ovhDrift,
        rps_drift_pct: rpsDrift,
      };
      if (d && ovhDrift !== null && Math.abs(ovhDrift) >= DRIFT_THRESHOLD_PCT) {
        report.significant_drift.push({
          profile,
          mode,
          documented_ovh_ms: d.ovh_avg,
          measured_ovh_ms: m.ovh_avg,
          drift_pct: ovhDrift,
          direction: ovhDrift < 0 ? 'faster than documented' : 'slower than documented',
        });
      }
    }
    report.profiles[profile] = { modes: rows };
  }

  console.log('================================================================');
  console.log(' measured matrix vs docs/benchmarks.md');
  console.log('================================================================');
  for (const [profile, data] of Object.entries(report.profiles)) {
    if (!data.modes) continue;
    console.log(`\n### ${profile}`);
    if (data.note) {
      console.log(`  (${data.note})`);
      console.log(`${'mode'.padEnd(32)} ${'meas ovh'.padStart(9)} ${'meas rps'.padStart(9)}`);
      for (const [mode, m] of Object.entries(data.modes)) {
        console.log(`${mode.slice(0, 32).padEnd(32)} ${String(m.ovh_avg).padStart(9)} ${String(m.rps).padStart(9)}`);
      }
      continue;
    }
    console.log(
      `${'mode'.padEnd(32)} ${'doc ovh'.padStart(8)} ${'meas ovh'.padStart(9)} ${'drift'.padStart(9)} ${'doc rps'.padStart(8)} ${'meas rps'.padStart(9)}`
    );
    for (const [mode, r] of Object.entries(data.modes)) {
      const d = r.documented;
      console.log(
        `${mode.slice(0, 32).padEnd(32)} ${String(d?.ovh_avg ?? '—').padStart(8)} ${String(r.measured.ovh_avg).padStart(9)} ` +
          `${(r.ovh_drift_pct === null ? '—' : (r.ovh_drift_pct > 0 ? '+' : '') + r.ovh_drift_pct + '%').padStart(9)} ` +
          `${String(d?.rps ?? '—').padStart(8)} ${String(r.measured.rps).padStart(9)}`
      );
    }
  }

  if (report.significant_drift.length) {
    console.log(`\n--- drift >= ${DRIFT_THRESHOLD_PCT}% on overhead ---`);
    for (const d of report.significant_drift) {
      console.log(
        `  ${d.profile}/${d.mode}: documented ${d.documented_ovh_ms}ms, measured ${d.measured_ovh_ms}ms ` +
          `(${d.drift_pct > 0 ? '+' : ''}${d.drift_pct}%, ${d.direction})`
      );
    }
  }

  fs.mkdirSync('test/results', { recursive: true });
  const out = path.join('test/results', 'gateway_matrix_report.json');
  fs.writeFileSync(out, JSON.stringify(report, null, 2), 'utf-8');
  console.log(`\nSaved ${out}`);
}

main();
