// Сравнение двух отчётов squozebench по ОЦЕНИВАЕМЫМ измерениям: вердикт, доля
// сжатия, recall иголок, идемпотентность, детерминизм. Отвечает на один вопрос —
// «эти два прогона судят корпус одинаково или нет».
//
//   node test/squozebench/repro/cmp_reports.mjs a.json b.json
//   node test/squozebench/repro/cmp_reports.mjs --expect-identical a.json b.json
//
// С флагом различие оценок — ненулевой код возврата, то есть проверяемое
// утверждение, а не абзац в документации. Латентность движка и поле
// squoze_version печатаются как происхождение прогона и различием НЕ считаются:
// p95 гуляет от загрузки хоста, а снимок рабочего дерева носит версию
// последнего тега.
//
// Это не замена cmp_savings.mjs: тот считает медиану экономии по каноничной
// паре base/head, здесь — построчная сверка вердиктов любых двух отчётов.
import { readFileSync } from 'node:fs';

const argv = process.argv.slice(2);
const strict = argv.includes('--expect-identical');
const paths = argv.filter((a) => a !== '--expect-identical');
if (paths.length !== 2) {
  console.error('usage: node test/squozebench/repro/cmp_reports.mjs [--expect-identical] a.json b.json');
  process.exit(2);
}

const load = (p) => JSON.parse(readFileSync(p, 'utf8'));
const [A, B] = paths.map(load);
const idx = (r) => Object.fromEntries((r.cases ?? []).map((c) => [c.name, c]));
const a = idx(A), b = idx(B);
const p95max = (r) => {
  if (Number.isFinite(r.summary?.max_latency_p95_ms)) return r.summary.max_latency_p95_ms;
  const xs = (r.cases ?? []).map((c) => c.latency_p95_ms).filter((v) => Number.isFinite(v));
  return xs.length ? Math.max(...xs) : NaN;
};

const label = (p, r) => p + '  squoze_version=' + (r.squoze_version ?? '?') +
  '  worst engine p95=' + p95max(r).toFixed(2) + 'ms';
console.log(label(paths[0], A));
console.log(label(paths[1], B));
for (const [p, r] of [[paths[0], A], [paths[1], B]]) {
  const s = r.summary ?? {};
  console.log('  ' + p + ': ' + s.cases_pass + ' pass / ' + s.cases_fail + ' fail / ' +
    s.cases_known_limit + ' known-limit, touched ' + s.cases_touched + '/' + s.cases_total +
    ', median savings when hit ' + s.median_savings_pct_when_hit + '%' +
    ', prefix_broken=' + JSON.stringify(s.prefix_broken_models ?? []));
}

const names = [...new Set([...Object.keys(a), ...Object.keys(b)])].sort();
const diffs = [];
for (const n of names) {
  const x = a[n], y = b[n];
  if (!x || !y) { diffs.push(n + ': кейс есть только в одном отчёте'); continue; }
  const f = [];
  const cmp = (what, u, v) => { if (u !== v) f.push(what + ' ' + u + ' -> ' + v); };
  cmp('verdict', x.verdict, y.verdict);
  if (Math.abs((x.savings_pct ?? 0) - (y.savings_pct ?? 0)) >= 0.05) {
    f.push('savings ' + x.savings_pct + '% -> ' + y.savings_pct + '%');
  }
  cmp('needle recall', x.needles?.recall_pct, y.needles?.recall_pct);
  cmp('idempotent', x.idempotent, y.idempotent);
  cmp('deterministic', x.deterministic, y.deterministic);
  if (f.length) diffs.push(n + ': ' + f.join('; '));
}

console.log('--- ' + names.length + ' кейсов сверено по вердикту, экономии, иголкам, идемпотентности, детерминизму ---');
if (!diffs.length) {
  console.log('IDENTICAL: ни одного различия в оценках');
} else {
  for (const d of diffs) console.log('  ' + d);
  console.log(diffs.length + ' различий');
}
if (strict && diffs.length) process.exit(1);
