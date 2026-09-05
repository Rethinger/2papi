// Сравнение двух отчётов харнесса, снятых savings_ab.sh. Знаменатель — только
// кейсы, которые сжали ОБЕ версии: доля от Touched каждой версии по отдельности
// меняет знаменатель вместе с числителем и потому несопоставима.
//
//   node test/squozebench/repro/cmp_savings.mjs
//
// Пути берутся от самого скрипта, так что запускать можно из любого каталога.
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const at = (rel) => fileURLToPath(new URL(rel, import.meta.url));
const load = (p) => JSON.parse(readFileSync(p, 'utf8'));
const base = load(at('../../results/squoze_ab/squoze_quality_report.base.json'));
const head = load(at('../../results/squoze_ab/squoze_quality_report.head.json'));

const idx = (r) => Object.fromEntries((r.cases ?? r.Cases ?? []).map((c) => [c.name, c]));
const B = idx(base), H = idx(head);

const median = (xs) => {
  if (!xs.length) return NaN;
  const s = [...xs].sort((a, b) => a - b);
  const m = s.length >> 1;
  return s.length % 2 ? s[m] : (s[m - 1] + s[m]) / 2;
};

const rows = [];
for (const name of Object.keys(H)) {
  const b = B[name], h = H[name];
  if (!b) continue;
  rows.push({
    name,
    class: h.class,
    basePct: b.savings_pct,
    headPct: h.savings_pct,
    delta: h.savings_pct - b.savings_pct,
    needlesB: b.needles?.recall_pct ?? b.needles?.RecallPct,
    needlesH: h.needles?.recall_pct ?? h.needles?.RecallPct,
    idemH: h.idempotent,
    fmtH: h.format?.valid ?? h.format?.Valid,
  });
}
rows.sort((a, b) => b.delta - a.delta);

const pad = (s, n) => String(s).padEnd(n);
const num = (v, n = 6) => (Number.isFinite(v) ? v.toFixed(1) : '  -  ').padStart(n);

console.log(pad('case', 26), pad('class', 12), 'base%   head%   delta   needles  idem fmt');
console.log('-'.repeat(84));
for (const r of rows) {
  // Падение до нуля на must-not-touch — это выполненный контракт, а не потеря:
  // базовая версия элидировала исходник, head его не трогает. Без отдельной
  // метки такая строка читается как регресс и портит вывод.
  const protectedNow = r.class === 'must-not-touch' && r.headPct === 0 && r.basePct > 0;
  const flag = protectedNow
    ? ' <= protected (expected)'
    : Math.abs(r.delta) >= 0.05 ? (r.delta > 0 ? ' <= gain' : ' <= LOSS') : '';
  console.log(
    pad(r.name, 26), pad(r.class ?? '', 12),
    num(r.basePct), num(r.headPct), num(r.delta, 7),
    num(r.needlesH, 8).padStart(8),
    String(r.idemH ?? '').padStart(5), String(r.fmtH ?? '').padStart(5), flag,
  );
}

const bm = median(rows.map((r) => r.basePct));
const hm = median(rows.map((r) => r.headPct));
// Общее подмножество: кейсы, которые сжимают ОБЕ версии. Это и есть знаменатель
// NFR-4. Сырая медиана по всем 15 кейсам сползает вниз ровно на два must-not-touch
// блоба, которые head перестал элидировать, — то есть на выполненный контракт.
const both = rows.filter((r) => r.basePct > 0 && r.headPct > 0);
const lbm = median(both.map((r) => r.basePct));
const lhm = median(both.map((r) => r.headPct));
// Только те, где base что-то элидировал, а head перестал: кейс, нулевой на
// обеих сторонах, никто не защищал — его просто никогда не сжимали.
const prot = rows.filter((r) => r.class === 'must-not-touch' && r.headPct === 0 && r.basePct > 0);
console.log('-'.repeat(84));
console.log(`median (all ${rows.length} cases)      base=${bm.toFixed(2)}%  head=${hm.toFixed(2)}%  delta=${(hm - bm).toFixed(2)}pp`);
console.log(`median (both compress, n=${both.length})  base=${lbm.toFixed(2)}%  head=${lhm.toFixed(2)}%  delta=${(lhm - lbm).toFixed(2)}pp   <= NFR-4`);
console.log(`improved: ${rows.filter((r) => r.delta > 0.05 && !(r.class === 'must-not-touch' && r.headPct === 0 && r.basePct > 0)).length}   regressed: ${rows.filter((r) => r.delta < -0.05 && !(r.class === 'must-not-touch' && r.headPct === 0 && r.basePct > 0)).length}   protected-now: ${prot.length}${prot.length ? ` (${prot.map((r) => r.name).join(', ')})` : ''}`);

const badNeedle = rows.filter((r) => Number.isFinite(r.needlesH) && r.needlesH < 100);
console.log(`needle recall < 100%: ${badNeedle.length ? badNeedle.map((r) => `${r.name}(${r.needlesH})`).join(', ') : 'none'}`);
const notIdem = rows.filter((r) => r.idemH === false);
console.log(`non-idempotent: ${notIdem.length ? notIdem.map((r) => r.name).join(', ') : 'none'}`);
const badFmt = rows.filter((r) => r.fmtH === false);
console.log(`format-invalid: ${badFmt.length ? badFmt.map((r) => r.name).join(', ') : 'none'}`);
