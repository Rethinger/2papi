// Live accuracy runner: does squoze change the model's answer?
//
// This is Level 2 of squoze's own docs/eval-protocol.md, which is marked
// unchecked there. The gates it defines are Δaccuracy <= 2pp, savings >= 50%,
// overhead < 15ms p95.
//
// Design notes that make this different from the existing suites:
//   * k repeats per condition (default 5). LLM sampling is stochastic; a single
//     run per condition measures variance, not effect. n=1 is refused outright.
//   * Paired comparison on identical inputs, alternating order to avoid drift.
//   * Preflight model availability check, so a provider outage is reported as
//     BLOCKED rather than silently becoming a "result".
//   * Reports median and IQR, not a single number.
//   * Grading is explicit and reported per trial, so a substring-match verdict
//     is never presented as "SWE-bench Verified resolved".
//
// Usage:
//   GATEWAY_URL=http://127.0.0.1:8989 GATEWAY_KEY=sk-2papi-bench \
//   MODEL_SQUOZE=claude-opus-5 MODEL_BASELINE=claude-opus-5-nosquoze \
//   REPEATS=5 node test/accuracy_suite.mjs

import fs from 'fs';
import path from 'path';
import { pathToFileURL } from 'url';

const BASE = process.env.GATEWAY_URL || 'http://127.0.0.1:8989';
const KEY = process.env.GATEWAY_KEY || 'sk-2papi-bench';
const MODEL_SQUOZE = process.env.MODEL_SQUOZE || 'claude-opus-5';
const MODEL_BASELINE = process.env.MODEL_BASELINE || 'claude-opus-5-nosquoze';
const REPEATS = Math.max(1, Number(process.env.REPEATS || 5));
const MAX_TOKENS = Number(process.env.MAX_TOKENS || 1500);
const GAP_MS = Number(process.env.GAP_MS || 3000);

const UA =
  'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36';

const FIXTURES = [
  'test/benchmarks/scenario1_deadlock.json',
  'test/benchmarks/scenario2_monorepo.json',
  'test/benchmarks/scenario3_multiturn.json',
  'test/benchmarks/swe_bench_django_16595.json',
  'test/benchmarks/swe_bench_flask_5014.json',
  'test/benchmarks/terminalbench_oom_crash.json',
  'test/benchmarks/aider_polyglot_rust_patch.json',
];

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function median(xs) {
  if (!xs.length) return null;
  const s = [...xs].sort((a, b) => a - b);
  const m = Math.floor(s.length / 2);
  return s.length % 2 ? s[m] : (s[m - 1] + s[m]) / 2;
}

function quantile(xs, q) {
  if (!xs.length) return null;
  const s = [...xs].sort((a, b) => a - b);
  const pos = (s.length - 1) * q;
  const lo = Math.floor(pos);
  const hi = Math.ceil(pos);
  return lo === hi ? s[lo] : s[lo] + (s[hi] - s[lo]) * (pos - lo);
}

function iqr(xs) {
  const q1 = quantile(xs, 0.25);
  const q3 = quantile(xs, 0.75);
  return q1 === null ? null : Number((q3 - q1).toFixed(2));
}

async function callGateway(model, messages) {
  const t0 = Date.now();
  let ttft = null;
  let content = '';
  let usage = null;

  // Kept, because the savings gate is a percentage: X-Gateway-Saved-Bytes alone
  // is a byte count, and a byte count without its denominator is not a rate.
  const bodyText = JSON.stringify({ model, messages, max_tokens: MAX_TOKENS, stream: true });
  const requestBytes = Buffer.byteLength(bodyText, 'utf-8');

  const res = await fetch(`${BASE}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${KEY}`,
      'User-Agent': UA,
    },
    body: bodyText,
  });

  const headers = {
    status: res.status,
    squoze: res.headers.get('x-gateway-squoze'),
    squozeLatencyMs: Number(res.headers.get('x-gateway-squoze-latency-ms') || 0),
    savedBytes: Number(res.headers.get('x-gateway-saved-bytes') || 0),
    overheadMs: Number(res.headers.get('x-gateway-overhead-ms') || 0),
    upstreamMs: Number(res.headers.get('x-gateway-upstream-ms') || 0),
  };

  if (!res.ok) {
    return { ok: false, requestBytes, headers, error: (await res.text()).slice(0, 300) };
  }

  const reader = res.body.getReader();
  const dec = new TextDecoder();
  let buf = '';
  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += dec.decode(value, { stream: true });
    const lines = buf.split('\n');
    buf = lines.pop();
    for (const line of lines) {
      const t = line.trim();
      if (!t.startsWith('data:')) continue;
      const payload = t.slice(5).trim();
      if (payload === '[DONE]') continue;
      try {
        const j = JSON.parse(payload);
        const d = j.choices?.[0]?.delta;
        if (d?.content) {
          if (ttft === null) ttft = Date.now() - t0;
          content += d.content;
        } else if (d?.reasoning_content && ttft === null) {
          ttft = Date.now() - t0;
        }
        if (j.usage) usage = j.usage;
      } catch {
        /* skip malformed frame */
      }
    }
  }

  return { ok: true, requestBytes, headers, usage, content, elapsedMs: Date.now() - t0, ttftMs: ttft };
}

// grade returns an explicit, auditable verdict. It deliberately does NOT claim
// benchmark resolution: substring presence is evidence about the answer's
// content, not a test-suite pass.
function grade(fixture, content) {
  const gt = fixture.ground_truth || {};
  const lower = content.toLowerCase();

  const patterns = gt.patterns || [];
  const keywords = gt.root_cause_keywords || [];
  const matchedPatterns = patterns.filter((p) => content.includes(p) || lower.includes(p.toLowerCase()));
  const matchedKeywords = keywords.filter((k) => lower.includes(k.toLowerCase()));

  const required = gt.required_patch_subsequence;
  const hasRequired = required ? content.includes(required) : null;
  const isDiff = /diff --git|^--- a\/|@@/m.test(content);

  return {
    method: 'substring-presence (NOT a test-suite execution)',
    patterns_matched: `${matchedPatterns.length}/${patterns.length}`,
    keywords_matched: `${matchedKeywords.length}/${keywords.length}`,
    required_subsequence_present: hasRequired,
    looks_like_unified_diff: isDiff,
    patterns_missing: patterns.filter((p) => !matchedPatterns.includes(p)),
    score:
      (patterns.length ? matchedPatterns.length / patterns.length : 1) *
      (hasRequired === null ? 1 : hasRequired ? 1 : 0),
  };
}

async function preflight() {
  const probe = await callGateway(MODEL_SQUOZE, [{ role: 'user', content: 'reply with: ok' }]).catch((e) => ({
    ok: false,
    error: String(e),
    headers: { status: 0 },
  }));
  if (probe.ok) return { ok: true };
  return { ok: false, status: probe.headers?.status, error: probe.error || 'unreachable' };
}

async function main() {
  console.log('================================================================');
  console.log(' Live accuracy suite — squoze vs baseline (paired, k repeats)');
  console.log(`  gateway=${BASE}  k=${REPEATS}  squoze=${MODEL_SQUOZE}  baseline=${MODEL_BASELINE}`);
  console.log('================================================================');

  if (REPEATS < 3) {
    console.error(`\nREPEATS=${REPEATS} is too low to separate effect from sampling variance.`);
    console.error('This runner refuses to produce a comparison below k=3. Set REPEATS>=3.');
    process.exit(2);
  }

  const report = {
    timestamp: new Date().toISOString(),
    suite: 'live accuracy: squoze vs baseline',
    gateway: BASE,
    models: { squoze: MODEL_SQUOZE, baseline: MODEL_BASELINE },
    repeats: REPEATS,
    protocol:
      'Paired per fixture: k repeats per condition, order alternated. Grading is ' +
      'substring presence against fixture ground_truth and is reported as such — ' +
      'it is not a SWE-bench/TerminalBench harness execution.',
    gates: { delta_accuracy_pp_max: 2, savings_pct_min: 50, overhead_p95_ms_max: 15 },
    status: 'PENDING',
    fixtures: [],
  };

  const pf = await preflight();
  if (!pf.ok) {
    report.status = 'BLOCKED';
    report.blocked_reason = `preflight failed: HTTP ${pf.status} ${String(pf.error).slice(0, 200)}`;
    console.error(`\nBLOCKED: ${report.blocked_reason}`);
    console.error('The upstream does not serve the configured model, so no accuracy claim can be made.');
    writeReport(report);
    return;
  }
  console.log('preflight ok\n');

  for (const fx of FIXTURES) {
    if (!fs.existsSync(fx)) {
      console.log(`  skip ${fx} (missing)`);
      continue;
    }
    const fixture = JSON.parse(fs.readFileSync(fx, 'utf-8'));
    const messages = [];
    if (fixture.system_instruction) messages.push({ role: 'system', content: fixture.system_instruction });
    messages.push(...fixture.messages);

    const label = fixture.instance_id || fixture.name || path.basename(fx);
    console.log(`── ${label}`);

    const trials = { squoze: [], baseline: [] };
    for (let i = 0; i < REPEATS; i++) {
      // Alternate which condition goes first to cancel provider drift.
      const order = i % 2 === 0 ? ['squoze', 'baseline'] : ['baseline', 'squoze'];
      for (const cond of order) {
        const model = cond === 'squoze' ? MODEL_SQUOZE : MODEL_BASELINE;
        const r = await callGateway(model, messages);
        if (r.ok) {
          const g = grade(fixture, r.content);
          trials[cond].push({
            score: g.score,
            elapsedMs: r.elapsedMs,
            ttftMs: r.ttftMs,
            promptTokens: r.usage?.prompt_tokens ?? null,
            completionTokens: r.usage?.completion_tokens ?? null,
            requestBytes: r.requestBytes,
            savedBytes: r.headers.savedBytes,
            savedPct: r.requestBytes ? Number(((r.headers.savedBytes / r.requestBytes) * 100).toFixed(2)) : null,
            overheadMs: r.headers.overheadMs,
            squozeActive: r.headers.squoze === 'true',
            squozeLatencyMs: r.headers.squozeLatencyMs,
            grade: g,
          });
          process.stdout.write(cond === 'squoze' ? 'S' : 'b');
        } else {
          trials[cond].push({ error: r.error, status: r.headers.status });
          process.stdout.write('!');
        }
        await sleep(GAP_MS);
      }
    }
    process.stdout.write('\n');

    const agg = (arr) => {
      const ok = arr.filter((t) => !t.error);
      const scores = ok.map((t) => t.score);
      const inTok = ok.map((t) => t.promptTokens).filter((v) => v !== null);
      return {
        n: arr.length,
        n_ok: ok.length,
        score_median: median(scores),
        score_iqr: iqr(scores),
        elapsed_median_ms: median(ok.map((t) => t.elapsedMs)),
        elapsed_iqr_ms: iqr(ok.map((t) => t.elapsedMs)),
        prompt_tokens_median: median(inTok),
        prompt_tokens_iqr: iqr(inTok),
        squoze_active_runs: ok.filter((t) => t.squozeActive).length,
        saved_bytes_median: median(ok.map((t) => t.savedBytes)),
        saved_pct_median: median(ok.map((t) => t.savedPct).filter((v) => v !== null)),
        squoze_latency_p95_ms: quantile(
          ok.map((t) => t.squozeLatencyMs).filter((v) => v !== null && v !== undefined),
          0.95
        ),
      };
    };

    const s = agg(trials.squoze);
    const b = agg(trials.baseline);
    const entry = {
      fixture: fx,
      label,
      squoze: s,
      baseline: b,
      delta_score_pp:
        s.score_median !== null && b.score_median !== null
          ? Number(((s.score_median - b.score_median) * 100).toFixed(2))
          : null,
      delta_prompt_tokens:
        s.prompt_tokens_median !== null && b.prompt_tokens_median !== null
          ? s.prompt_tokens_median - b.prompt_tokens_median
          : null,
      trials,
      caveat:
        s.squoze_active_runs === 0
          ? 'squoze did not fire in any run (size gate not crossed) — any difference here is sampling variance, not a squoze effect'
          : null,
    };
    report.fixtures.push(entry);

    console.log(
      `   squoze  : score median=${s.score_median} iqr=${s.score_iqr} tokens=${s.prompt_tokens_median} active_runs=${s.squoze_active_runs}/${s.n_ok}`
    );
    console.log(`   baseline: score median=${b.score_median} iqr=${b.score_iqr} tokens=${b.prompt_tokens_median}`);
    if (entry.caveat) console.log(`   ! ${entry.caveat}`);
  }

  evaluateGates(report);
  printGates(report);

  writeReport(report);
}

// Exported so the judging can be tested without a provider: the gates are the
// part of this runner that has to be right even when nothing is reachable.
// See test/accuracy_gates_selftest.mjs.
export function evaluateGates(report) {
  // ---- gates, judged rather than printed -------------------------------------
  // A report that lists thresholds next to numbers and leaves the comparison to
  // the reader has judged nothing. Each gate below resolves to pass/fail, or to
  // null with the reason it could not be judged.
  const fired = report.fixtures.filter((f) => (f.squoze.squoze_active_runs || 0) > 0);
  const deltas = fired.map((f) => f.delta_score_pp).filter((v) => v !== null);
  const savings = fired.map((f) => f.squoze.saved_pct_median).filter((v) => v !== null);
  // Only trials where squoze actually ran. A skipped request reports latency 0,
  // and averaging those in would report the overhead of not compressing.
  const latencies = report.fixtures.flatMap((f) =>
    f.trials.squoze
      .filter((t) => !t.error && t.squozeActive && typeof t.squozeLatencyMs === 'number')
      .map((t) => t.squozeLatencyMs)
  );

  const worstDelta = deltas.length ? Math.min(...deltas) : null;
  const medianSavings = savings.length ? median(savings) : null;
  const latencyP95 = latencies.length ? Number(quantile(latencies, 0.95).toFixed(2)) : null;

  report.gate_results = {
    fixtures_total: report.fixtures.length,
    fixtures_where_squoze_fired: fired.length,
    delta_accuracy: {
      worst_pp: worstDelta,
      threshold_pp: -report.gates.delta_accuracy_pp_max,
      verdict: worstDelta === null ? null : worstDelta >= -report.gates.delta_accuracy_pp_max ? 'pass' : 'fail',
      basis: 'worst per-fixture median score delta among fixtures where squoze fired',
    },
    savings: {
      median_pct: medianSavings,
      threshold_pct: report.gates.savings_pct_min,
      verdict: medianSavings === null ? null : medianSavings >= report.gates.savings_pct_min ? 'pass' : 'fail',
      basis: 'X-Gateway-Saved-Bytes over the request body actually sent, per trial, median of fixture medians',
    },
    overhead: {
      squoze_p95_ms: latencyP95,
      threshold_ms: report.gates.overhead_p95_ms_max,
      verdict: latencyP95 === null ? null : latencyP95 <= report.gates.overhead_p95_ms_max ? 'pass' : 'fail',
      basis: 'X-Gateway-Squoze-Latency-Ms across every squoze trial, p95',
    },
  };

  // Does the provider's own accounting mean anything? If prompt_tokens is the
  // same number for fixtures of wildly different sizes, it is a placeholder, and
  // delta_prompt_tokens computed from it is noise dressed as a measurement.
  const promptTokenValues = new Set(
    report.fixtures
      .flatMap((f) => [f.squoze.prompt_tokens_median, f.baseline.prompt_tokens_median])
      .filter((v) => v !== null && v !== undefined)
  );
  report.provider_usage = {
    distinct_prompt_tokens_values: promptTokenValues.size,
    values: [...promptTokenValues].slice(0, 12),
    usable_for_savings: promptTokenValues.size > 1,
    note:
      promptTokenValues.size > 1
        ? 'prompt_tokens varies with payload size, so delta_prompt_tokens is a measurement'
        : 'prompt_tokens is constant across fixtures of different sizes: the provider reports a placeholder, ' +
          'so delta_prompt_tokens says nothing and the savings gate is judged on gateway byte accounting instead',
  };

  const verdicts = Object.values(report.gate_results)
    .filter((g) => g && typeof g === 'object' && 'verdict' in g)
    .map((g) => g.verdict);
  if (!fired.length) {
    report.status = 'INCONCLUSIVE';
    report.inconclusive_reason =
      'squoze did not fire on any fixture, so there is no compression to judge — the fixtures are below the size gate';
  } else if (verdicts.includes('fail')) {
    report.status = 'FAIL';
  } else if (verdicts.includes(null)) {
    report.status = 'PARTIAL';
  } else {
    report.status = 'PASS';
  }
  return report;
}

function printGates(report) {
  console.log('');
  console.log('---- gates ----');
  for (const [name, g] of Object.entries(report.gate_results)) {
    if (!g || typeof g !== 'object' || !('verdict' in g)) continue;
    console.log(`  ${name.padEnd(15)} ${String(g.verdict ?? 'not judged').padEnd(10)} ${JSON.stringify(g)}`);
  }
  console.log(`  provider usage : ${report.provider_usage.note}`);
  console.log(`  status         : ${report.status}`);
}

function writeReport(report) {
  fs.mkdirSync('test/results', { recursive: true });
  const out = path.join('test/results', 'accuracy_report.json');
  fs.writeFileSync(out, JSON.stringify(report, null, 2), 'utf-8');
  console.log(`\nSaved ${out} (status: ${report.status})`);
}

// Only when run directly. Imported (by the gate self-test) this must do nothing.
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main();
}
