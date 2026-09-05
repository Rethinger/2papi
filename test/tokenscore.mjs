// Token scoring for the squoze quality report.
//
// squozebench measures bytes, which is what the gateway's X-Gateway-Saved-Bytes
// header reports — but providers bill tokens, and the two do not move together.
// Head/tail elision of highly repetitive text removes bytes that tokenize
// densely, so byte savings systematically overstate token savings.
//
// This reads the before/after artifacts and adds real tokenizer counts.
//
//   node test/tokenscore.mjs
//
// Anthropic publishes no local tokenizer, so Claude-family rows are scored with
// OpenAI's o200k_base as a documented proxy.

import fs from 'fs';
import path from 'path';

const REPORT = 'test/results/squoze_quality_report.json';

async function loadEncoder() {
  try {
    const mod = await import('gpt-tokenizer/encoding/o200k_base');
    return { encode: mod.encode, name: 'o200k_base' };
  } catch {
    try {
      const mod = await import('gpt-tokenizer');
      return { encode: mod.encode, name: 'cl100k_base' };
    } catch (err) {
      console.error('gpt-tokenizer is not installed. Run:');
      console.error('  npm install --no-save gpt-tokenizer');
      process.exit(2);
    }
  }
}

function pct(before, after) {
  if (!before) return 0;
  return Number((((before - after) / before) * 100).toFixed(2));
}

async function main() {
  if (!fs.existsSync(REPORT)) {
    console.error(`${REPORT} not found — run the Go harness first:`);
    console.error('  docker run --rm -v "$PWD:/src" -w /src golang:1.23 go run ./test/squozebench');
    process.exit(2);
  }
  const { encode, name } = await loadEncoder();
  const report = JSON.parse(fs.readFileSync(REPORT, 'utf-8'));

  console.log('================================================================');
  console.log(` token scoring with ${name} (Claude rows are a documented proxy)`);
  console.log('================================================================');
  console.log(
    `${'case'.padEnd(34)} ${'tok before'.padStart(11)} ${'tok after'.padStart(10)} ${'tok save'.padStart(9)} ${'byte save'.padStart(10)} ${'delta'.padStart(7)}`
  );

  let totalBefore = 0;
  let totalAfter = 0;
  const divergences = [];

  for (const c of report.cases) {
    if (!c.artifact_before || !fs.existsSync(c.artifact_before)) continue;
    const before = fs.readFileSync(c.artifact_before, 'utf-8');
    const after = fs.existsSync(c.artifact_after) ? fs.readFileSync(c.artifact_after, 'utf-8') : before;

    const tb = encode(before).length;
    const ta = encode(after).length;
    c.tokens_before = tb;
    c.tokens_after = ta;
    c.token_savings_pct = pct(tb, ta);
    c.tokenizer = name;
    c.tokenizer_is_proxy = (c.model_family || '').toLowerCase() === 'claude';

    totalBefore += tb;
    totalAfter += ta;

    const delta = Number((c.token_savings_pct - c.savings_pct).toFixed(2));
    if (Math.abs(delta) >= 5) {
      divergences.push({ name: c.name, byte: c.savings_pct, token: c.token_savings_pct, delta });
    }

    console.log(
      `${c.name.slice(0, 34).padEnd(34)} ${String(tb).padStart(11)} ${String(ta).padStart(10)} ` +
        `${(c.token_savings_pct + '%').padStart(9)} ${(c.savings_pct.toFixed(2) + '%').padStart(10)} ${(delta > 0 ? '+' : '') + delta}`
    );
  }

  report.token_summary = {
    tokenizer: name,
    note:
      'Anthropic ships no local tokenizer; Claude-family cases are scored with ' +
      name +
      ' as a proxy. Byte savings and token savings diverge because repetitive ' +
      'machine output tokenizes more densely than prose.',
    corpus_tokens_before: totalBefore,
    corpus_tokens_after: totalAfter,
    corpus_token_savings_pct: pct(totalBefore, totalAfter),
    byte_vs_token_divergences: divergences,
  };

  fs.writeFileSync(REPORT, JSON.stringify(report, null, 2), 'utf-8');

  console.log('\n----------------------------------------------------------------');
  console.log(
    ` corpus total: ${totalBefore} -> ${totalAfter} tokens (${report.token_summary.corpus_token_savings_pct}% saved)`
  );
  if (divergences.length) {
    console.log(' cases where byte savings misrepresent token savings by >=5pp:');
    for (const d of divergences) {
      console.log(`   - ${d.name}: bytes ${d.byte.toFixed(1)}% vs tokens ${d.token}% (${d.delta > 0 ? '+' : ''}${d.delta}pp)`);
    }
  }
  console.log(` updated ${REPORT}`);
  console.log('----------------------------------------------------------------');
}

main();
