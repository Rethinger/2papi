#!/usr/bin/env node
// Reproducible gateway benchmark (Ferro-style, открытая методология):
//   - fixed local fake upstream (no provider network in the loop);
//   - concurrency tiers, wall TTFB per request + the gateway's own
//     X-Gateway-Overhead-MS / X-Gateway-Upstream-MS headers;
//   - p50/p95/p99 report.
//
// Usage (from repo root):
//   docker compose --profile bench up --build bench-runner
//
// Two report shapes:
//   BENCH_MATRIX unset → concurrency tiers for one payload/mode (default).
//   BENCH_MATRIX=1     → optimization-mode matrix at fixed concurrency:
//                        baseline vs every rtk/caveman/headroom/auto mode,
//                        with the delta against baseline (виток 8).
//
// Env overrides: GATEWAY_URL, BENCH_KEY, BENCH_TIERS ("10,50,100"),
// BENCH_DURATION_MS (per tier), BENCH_PAYLOAD (small|large|huge),
// BENCH_MATRIX_CONC, BENCH_SQUOZE_MODEL.

const GATEWAY_URL = process.env.GATEWAY_URL ?? 'http://gateway-bench:8080';
const KEY = process.env.BENCH_KEY ?? 'sk-gateway-dev';
const TIERS = (process.env.BENCH_TIERS ?? '10,50,100').split(',').map(s => Number(s.trim())).filter(n => n > 0);
const DURATION_MS = Number(process.env.BENCH_DURATION_MS ?? 8000);
const PAYLOAD = process.env.BENCH_PAYLOAD ?? 'small';
const MODEL = process.env.BENCH_MODEL ?? 'bench-model';
const SQUOZE_MODEL = process.env.BENCH_SQUOZE_MODEL ?? 'bench-squoze';

// Payload profiles. Optimizers are size-gated (RTK skips bodies < 2 KiB,
// headroom prunes only above an 80-150k estimated-token reserve), so a single
// tiny body would report "no overhead" for every mode and prove nothing.
//   small — pure gateway overhead, every optimizer short-circuits;
//   large — ~64 KiB with tool_result blocks: RTK and caveman do real work;
//   huge  — ~420 KiB across many turns: also crosses the headroom reserve.
function buildBody(profile, model) {
  if (profile === 'small') {
    return JSON.stringify({ model, messages: [{ role: 'user', content: 'hi' }], max_tokens: 5 });
  }
  const toolChunk = size => JSON.stringify({
    status: 'ok',
    rows: Array.from({ length: Math.ceil(size / 96) }, (_, i) => ({
      id: i,
      path: `internal/module_${i}/handler.go`,
      diagnostic: 'unused variable shadows package-level identifier in the retry branch',
    })),
  });
  const turns = profile === 'huge' ? 60 : 8;
  const chunkBytes = profile === 'huge' ? 7000 : 8000;
  const messages = [{ role: 'system', content: 'You are a coding agent operating on a large Go repository.' }];
  for (let t = 0; t < turns; t += 1) {
    messages.push({ role: 'user', content: `Turn ${t}: inspect the failing package and summarise the root cause.` });
    messages.push({
      role: 'assistant',
      content: [{ type: 'text', text: `Calling the inspector for turn ${t}.` }],
      tool_calls: [{ id: `call_${t}`, type: 'function', function: { name: 'inspect', arguments: '{"pkg":"internal"}' } }],
    });
    messages.push({ role: 'tool', tool_call_id: `call_${t}`, content: toolChunk(chunkBytes) });
  }
  messages.push({ role: 'user', content: 'Now give the final summary.' });
  return JSON.stringify({ model, messages, max_tokens: 64 });
}

let body = buildBody(PAYLOAD, MODEL);

function percentile(sorted, p) {
  if (!sorted.length) return 0;
  const idx = Math.min(sorted.length - 1, Math.ceil((p / 100) * sorted.length) - 1);
  return sorted[Math.max(0, idx)];
}

async function oneRequest(extraHeaders = {}, payload = body) {
  const started = performance.now();
  const res = await fetch(`${GATEWAY_URL}/v1/chat/completions`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', authorization: `Bearer ${KEY}`, ...extraHeaders },
    body: payload,
  });
  const firstByte = performance.now();
  await res.arrayBuffer();
  const done = performance.now();
  return {
    ok: res.status === 200,
    status: res.status,
    ttfb: firstByte - started,
    total: done - started,
    overhead: Number(res.headers.get('x-gateway-overhead-ms') ?? NaN),
    upstream: Number(res.headers.get('x-gateway-upstream-ms') ?? NaN),
    echo: {
      // Echo header names are not uniform: RTK reports the mode it chose
      // (X-Gateway-RTK-Mode), the others report a boolean and are only set when
      // the pass actually did something.
      rtk: res.headers.get('x-gateway-rtk-mode') ?? '',
      caveman: res.headers.get('x-gateway-caveman-mode') || res.headers.get('x-gateway-caveman') || '',
      headroom: res.headers.get('x-gateway-headroom-profile') || res.headers.get('x-gateway-headroom') || '',
      squoze: res.headers.get('x-gateway-squoze') ?? '',
    },
  };
}

async function tier(concurrency, extraHeaders = {}, payload = body) {
  const deadline = performance.now() + DURATION_MS;
  const samples = [];
  let errors = 0;
  const stats = { overhead: [], upstream: [] };
  let echo = null;

  async function worker() {
    while (performance.now() < deadline) {
      try {
        const r = await oneRequest(extraHeaders, payload);
        if (!r.ok) { errors += 1; continue; }
        echo ??= r.echo;
        samples.push(r.ttfb);
        if (Number.isFinite(r.overhead)) stats.overhead.push(r.overhead);
        if (Number.isFinite(r.upstream)) stats.upstream.push(r.upstream);
      } catch {
        errors += 1;
      }
    }
  }
  await Promise.all(Array.from({ length: concurrency }, worker));
  if (errors > 0 && samples.length === 0) {
    // First hard failure with root cause — makes broken setups obvious.
    try {
      const probe = await oneRequest(extraHeaders, payload);
      console.log(`\nprobe: status=${probe.status} ttfb=${Math.round(probe.ttfb)}ms`);
      const res = await fetch(`${GATEWAY_URL}/v1/chat/completions`, {
        method: 'POST',
        headers: { 'content-type': 'application/json', authorization: `Bearer ${KEY}`, ...extraHeaders },
        body: payload,
      });
      console.log('probe body:', (await res.text()).slice(0, 300));
    } catch (e) {
      console.log(`probe fetch failed: ${e.message} ${e.cause?.message ?? ''}`);
    }
  }

  samples.sort((a, b) => a - b);
  const avg = arr => (arr.length ? arr.reduce((s, v) => s + v, 0) / arr.length : 0);
  const rps = samples.length / (DURATION_MS / 1000);
  return {
    concurrency,
    requests: samples.length,
    errors,
    rps: Math.round(rps),
    ttfb_p50: Math.round(percentile(samples, 50)),
    ttfb_p95: Math.round(percentile(samples, 95)),
    ttfb_p99: Math.round(percentile(samples, 99)),
    overhead_avg_ms: Number(avg(stats.overhead).toFixed(2)),
    overhead_p95_ms: Math.round(percentile([...stats.overhead].sort((a, b) => a - b), 95)),
    upstream_avg_ms: Math.round(avg(stats.upstream)),
    echo,
  };
}

function print(rows) {
  const header = ['conc', 'reqs', 'rps', 'err', 'ttfb_p50', 'ttfb_p95', 'ttfb_p99', 'ovh_avg', 'ovh_p95', 'up_avg'];
  const width = [6, 7, 6, 4, 9, 9, 9, 8, 8, 8];
  const line = cells => cells.map((c, i) => String(c).padStart(width[i])).join(' | ');
  console.log(line(header));
  console.log(width.map(w => '-'.repeat(w)).join('-+-'));
  for (const row of rows) {
    console.log(line([row.concurrency, row.requests, row.rps, row.errors, row.ttfb_p50, row.ttfb_p95, row.ttfb_p99, row.overhead_avg_ms, row.overhead_p95_ms, row.upstream_avg_ms]));
  }
  console.log('\nAll values in ms except conc/reqs/rps/err. ovh_* = gateway self-reported');
  console.log('overhead (X-Gateway-Overhead-MS), up_avg = upstream time it reports.');
}

// ---------------------------------------------------------------- виток 8 ---
// Mode matrix. Modes are selected per request by header (X-Gateway-RTK /
// -Caveman / -Headroom accept a mode name), so every row runs against the same
// warm process — the delta is the mode's cost, not container variance. Squoze
// has no header (config-exclusive), so it rides a second model alias.
// RTK's per-request header is X-Gateway-Compress (X-Gateway-Compression is
// accepted as an alias); there is no X-Gateway-RTK. RTK echoes back as
// X-Gateway-RTK-Mode, which is why the two names differ here.
const MATRIX = [
  { label: 'baseline (off)', headers: {} },
  { label: 'rtk light', headers: { 'X-Gateway-Compress': 'light' } },
  { label: 'rtk standard', headers: { 'X-Gateway-Compress': 'standard' } },
  { label: 'rtk aggressive', headers: { 'X-Gateway-Compress': 'aggressive' } },
  { label: 'rtk auto', headers: { 'X-Gateway-Compress': 'auto' } },
  { label: 'caveman lite', headers: { 'X-Gateway-Caveman': 'lite' } },
  { label: 'caveman full', headers: { 'X-Gateway-Caveman': 'full' } },
  { label: 'caveman auto', headers: { 'X-Gateway-Caveman': 'auto' } },
  { label: 'headroom conservative', headers: { 'X-Gateway-Headroom': 'conservative' } },
  { label: 'headroom balanced', headers: { 'X-Gateway-Headroom': 'balanced' } },
  { label: 'headroom aggressive', headers: { 'X-Gateway-Headroom': 'aggressive' } },
  { label: 'headroom auto', headers: { 'X-Gateway-Headroom': 'auto' } },
  {
    label: 'all three (std/full/balanced)',
    headers: { 'X-Gateway-Compress': 'standard', 'X-Gateway-Caveman': 'full', 'X-Gateway-Headroom': 'balanced' },
  },
  { label: 'squoze (exclusive)', headers: {}, model: SQUOZE_MODEL },
];

async function matrix() {
  const conc = Number(process.env.BENCH_MATRIX_CONC ?? 20);
  const profiles = (process.env.BENCH_PAYLOADS ?? 'small,large,huge').split(',').map(s => s.trim()).filter(Boolean);

  for (const profile of profiles) {
    const bodies = new Map();
    const bodyFor = model => {
      if (!bodies.has(model)) bodies.set(model, buildBody(profile, model));
      return bodies.get(model);
    };
    const bytes = Buffer.byteLength(bodyFor(MODEL));
    console.log(`\n### payload=${profile} (${(bytes / 1024).toFixed(1)} KiB) conc=${conc} duration=${DURATION_MS}ms/mode`);

    const rows = [];
    for (const entry of MATRIX) {
      const payload = bodyFor(entry.model ?? MODEL);
      // Warm this mode's path before measuring it.
      await Promise.all(Array.from({ length: 3 }, () => oneRequest(entry.headers, payload).catch(() => {})));
      const r = await tier(conc, entry.headers, payload);
      rows.push({ ...r, label: entry.label });
      process.stdout.write(`\r  ${entry.label.padEnd(30)} ovh_avg=${r.overhead_avg_ms}ms err=${r.errors}   \n`);
    }

    const base = rows[0];
    const header = ['mode', 'reqs', 'rps', 'err', 'ovh_avg', 'ovh_p95', 'Δ ovh_avg', 'ttfb_p95', 'applied'];
    const width = [30, 7, 6, 4, 8, 8, 10, 9, 22];
    const line = cells => cells.map((c, i) => String(c).padEnd(width[i])).join(' | ');
    console.log('');
    console.log(line(header));
    console.log(width.map(w => '-'.repeat(w)).join('-+-'));
    for (const row of rows) {
      const delta = row === base ? '—' : `${row.overhead_avg_ms - base.overhead_avg_ms >= 0 ? '+' : ''}${(row.overhead_avg_ms - base.overhead_avg_ms).toFixed(2)}`;
      const applied = Object.entries(row.echo ?? {}).filter(([, v]) => v && v !== 'false').map(([k, v]) => `${k}=${v}`).join(' ') || '—';
      console.log(line([row.label, row.requests, row.rps, row.errors, row.overhead_avg_ms, row.overhead_p95_ms, delta, row.ttfb_p95, applied.slice(0, 22)]));
    }
    console.log('\nΔ ovh_avg = mode overhead minus baseline overhead (ms, gateway self-reported).');
    console.log('applied = echo headers the gateway set, i.e. what it actually ran.');
  }
}

(async () => {
  if (process.env.BENCH_MATRIX === '1') {
    await Promise.all(Array.from({ length: 5 }, () => oneRequest().catch(() => {})));
    await matrix();
    return;
  }
  console.log(`payload=${PAYLOAD} (${(Buffer.byteLength(body) / 1024).toFixed(1)} KiB) model=${MODEL}`);
  // Warm-up: JIT + connection pool + snapshot adoption.
  await Promise.all(Array.from({ length: 5 }, () => oneRequest().catch(() => {})));
  const rows = [];
  for (const c of TIERS) {
    process.stdout.write(`\rrunning tier conc=${c} …`);
    rows.push(await tier(c));
    process.stdout.write('\n');
  }
  print(rows);
})();
