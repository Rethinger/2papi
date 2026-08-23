#!/usr/bin/env node
// Reproducible gateway benchmark (Ferro-style, открытая методология):
//   - fixed local fake upstream (no provider network in the loop);
//   - concurrency tiers, wall TTFB per request + the gateway's own
//     X-Gateway-Overhead-MS / X-Gateway-Upstream-MS headers;
//   - p50/p95/p99 report.
//
// Usage (from repo root):
//   docker compose --profile bench up --build bench-runner
// Env overrides: GATEWAY_URL, BENCH_KEY, BENCH_TIERS ("10,50,100"),
// BENCH_DURATION_MS (per tier).

const GATEWAY_URL = process.env.GATEWAY_URL ?? 'http://gateway-bench:8080';
const KEY = process.env.BENCH_KEY ?? 'sk-gateway-dev';
const TIERS = (process.env.BENCH_TIERS ?? '10,50,100').split(',').map(s => Number(s.trim())).filter(n => n > 0);
const DURATION_MS = Number(process.env.BENCH_DURATION_MS ?? 8000);

const body = JSON.stringify({ model: 'bench-model', messages: [{ role: 'user', content: 'hi' }], max_tokens: 5 });

function percentile(sorted, p) {
  if (!sorted.length) return 0;
  const idx = Math.min(sorted.length - 1, Math.ceil((p / 100) * sorted.length) - 1);
  return sorted[Math.max(0, idx)];
}

async function oneRequest() {
  const started = performance.now();
  const res = await fetch(`${GATEWAY_URL}/v1/chat/completions`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', authorization: `Bearer ${KEY}` },
    body,
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
  };
}

async function tier(concurrency) {
  const deadline = performance.now() + DURATION_MS;
  const samples = [];
  let errors = 0;
  const stats = { overhead: [], upstream: [] };

  async function worker() {
    while (performance.now() < deadline) {
      try {
        const r = await oneRequest();
        if (!r.ok) { errors += 1; continue; }
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
      const probe = await oneRequest();
      console.log(`\nprobe: status=${probe.status} ttfb=${Math.round(probe.ttfb)}ms`);
      const res = await fetch(`${GATEWAY_URL}/v1/chat/completions`, {
        method: 'POST',
        headers: { 'content-type': 'application/json', authorization: `Bearer ${KEY}` },
        body,
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

(async () => {
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
