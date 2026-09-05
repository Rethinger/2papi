#!/usr/bin/env node
// Probe gpt.crax.lol: confirm each model actually returns content, and record
// whether it honours stream:false and whether usage is reported.
//
// Key comes from CRAX_KEY env only. Never hardcode it here.
//
//   CRAX_KEY=... node test/crax_probe.mjs
//   CRAX_KEY=... MODELS=claude-sonnet-5,gpt-5-5 node test/crax_probe.mjs

import { writeFileSync, mkdirSync } from 'node:fs';

const KEY = process.env.CRAX_KEY;
const BASE = process.env.CRAX_URL || 'https://gpt.crax.lol';
const GAP_MS = Number(process.env.PROBE_GAP_MS || 2500);

if (!KEY) {
  console.error('CRAX_KEY is required (never hardcode the key)');
  process.exit(2);
}

const UA =
  'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 ' +
  '(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36';

const headers = {
  Authorization: `Bearer ${KEY}`,
  'Content-Type': 'application/json',
  'User-Agent': UA,
};

// Parse an SSE body into concatenated content + the last usage object seen.
function parseSSE(raw) {
  let content = '';
  let usage = null;
  let sawDone = false;
  for (const line of raw.split(/\r?\n/)) {
    if (!line.startsWith('data:')) continue;
    const payload = line.slice(5).trim();
    if (payload === '[DONE]') {
      sawDone = true;
      continue;
    }
    try {
      const j = JSON.parse(payload);
      const d = j.choices?.[0];
      content += d?.delta?.content ?? d?.message?.content ?? '';
      if (j.usage) usage = j.usage;
    } catch {
      /* partial frame, ignore */
    }
  }
  return { content, usage, sawDone };
}

function parseBody(raw) {
  const trimmed = raw.trimStart();
  if (trimmed.startsWith('data:')) {
    const r = parseSSE(raw);
    return { ...r, transport: 'sse' };
  }
  try {
    const j = JSON.parse(raw);
    return {
      content: j.choices?.[0]?.message?.content ?? '',
      usage: j.usage ?? null,
      sawDone: false,
      transport: 'json',
      error: j.error?.message ?? null,
    };
  } catch {
    return { content: '', usage: null, sawDone: false, transport: 'unparsed', raw: raw.slice(0, 200) };
  }
}

async function listModels() {
  const r = await fetch(`${BASE}/v1/models`, { headers });
  if (!r.ok) throw new Error(`models: HTTP ${r.status}`);
  const j = await r.json();
  return (j.data || []).map((m) => ({ id: m.id, ctx: m.context_length, provider: m.provider }));
}

async function probe(model) {
  const t0 = Date.now();
  let res;
  try {
    res = await fetch(`${BASE}/v1/chat/completions`, {
      method: 'POST',
      headers,
      body: JSON.stringify({
        model,
        messages: [{ role: 'user', content: 'Reply with exactly: OK42' }],
        max_tokens: 16,
        temperature: 0,
        stream: false,
      }),
      signal: AbortSignal.timeout(120_000),
    });
  } catch (e) {
    return { model, ok: false, error: String(e.message || e), ms: Date.now() - t0 };
  }
  const raw = await res.text();
  const ms = Date.now() - t0;
  const p = parseBody(raw);
  const ok = res.ok && p.content.length > 0;
  return {
    model,
    ok,
    http: res.status,
    ms,
    transport: p.transport,
    honours_stream_false: p.transport === 'json',
    content: p.content.slice(0, 80),
    said_ok42: /OK42/.test(p.content),
    usage: p.usage,
    error: p.error ?? p.raw ?? null,
  };
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const models = await listModels();
const wanted = process.env.MODELS ? process.env.MODELS.split(',').map((s) => s.trim()) : models.map((m) => m.id);

console.log(`provider ${BASE} lists ${models.length} models; probing ${wanted.length}\n`);
console.log('model                     http  ms     transport  content            usage');
console.log('-'.repeat(88));

const results = [];
for (const id of wanted) {
  const r = await probe(id);
  results.push(r);
  const u = r.usage ? `${r.usage.prompt_tokens}/${r.usage.completion_tokens}` : '—';
  console.log(
    `${id.padEnd(25)} ${String(r.http ?? '—').padEnd(5)} ${String(r.ms).padEnd(6)} ` +
      `${(r.transport ?? '—').padEnd(10)} ${(r.ok ? r.content.replace(/\s+/g, ' ') : 'FAIL ' + (r.error ?? '')).slice(0, 18).padEnd(18)} ${u}`,
  );
  await sleep(GAP_MS);
}

const working = results.filter((r) => r.ok);
const honours = results.filter((r) => r.honours_stream_false);
const withUsage = results.filter((r) => r.usage);

console.log('-'.repeat(88));
console.log(`working: ${working.length}/${results.length}`);
console.log(`honours stream:false: ${honours.length}/${results.length}`);
console.log(`reports usage: ${withUsage.length}/${results.length}`);

mkdirSync('test/results', { recursive: true });
const out = {
  generated_at: new Date().toISOString(),
  base_url: BASE,
  listed_models: models,
  probed: results,
  summary: {
    working: working.length,
    total: results.length,
    working_ids: working.map((r) => r.model),
    honours_stream_false: honours.length,
    reports_usage: withUsage.length,
  },
};
writeFileSync('test/results/crax_probe.json', JSON.stringify(out, null, 2));
console.log('\nwrote test/results/crax_probe.json');
