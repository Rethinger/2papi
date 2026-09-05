// Provider capability probe: which models does the upstream actually serve,
// and does it report token usage consistently?
//
// Usage:
//   PROBE_KEY=sk-... node test/provider_probe.mjs [--base=https://gorouter.app/v1]
//
//   PROBE_KEY   upstream API key (required; never hard-coded, never in a report)
//   PROBE_BASE  base URL including the version prefix, or --base=
//   PROBE_MODELS  comma-separated catalogue to probe (default: DEFAULT_CANDIDATES)
//   PROBE_OUT   report file name under test/results, or --out=
//   PROBE_GAP_MS  pause between calls (default 6000)
//
// Writes test/results/<PROBE_OUT>, default provider_probe.json

import fs from 'fs';
import path from 'path';

const BASE = (process.argv.find((a) => a.startsWith('--base='))?.slice(7)) || process.env.PROBE_BASE || 'https://gorouter.app/v1';
const KEY = process.env.PROBE_KEY;
if (!KEY) {
  console.error('PROBE_KEY env var is required (upstream API key).');
  process.exit(2);
}

// One report per provider. A second provider probed into the same file would
// silently delete the evidence for the first, and both are cited by
// test/results/README.md.
const OUT_NAME =
  process.argv.find((a) => a.startsWith('--out='))?.slice(6) || process.env.PROBE_OUT || 'provider_probe.json';

const UA =
  'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36';

const DEFAULT_CANDIDATES = [
  'claude-opus-5',
  'claude-opus-4-5',
  'claude-opus-4-1',
  'claude-sonnet-4-5',
  'claude-sonnet-4',
  'claude-3-7-sonnet',
  'claude-3-5-sonnet',
  'gpt-5',
  'gpt-5-mini',
  'gpt-4o',
  'gpt-4o-mini',
  'gemini-2.5-pro',
  'gemini-2.5-flash',
  'deepseek-chat',
  'deepseek-reasoner',
];

// PROBE_MODELS points the same runner at a provider with a different catalogue.
// Forking the file per provider would fork the probe logic with it.
const CANDIDATES = (process.env.PROBE_MODELS || '').trim()
  ? process.env.PROBE_MODELS.split(',').map((m) => m.trim()).filter(Boolean)
  : DEFAULT_CANDIDATES;

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const GAP_MS = Number(process.env.PROBE_GAP_MS || 6000);

function headers() {
  return {
    Authorization: `Bearer ${KEY}`,
    'Content-Type': 'application/json',
    'User-Agent': UA,
    Accept: 'application/json',
  };
}

async function listModels() {
  try {
    const res = await fetch(`${BASE}/models`, { headers: headers() });
    const text = await res.text();
    let ids = [];
    try {
      const j = JSON.parse(text);
      ids = (j.data || []).map((m) => m.id);
    } catch {
      /* non-JSON (Cloudflare page) */
    }
    return { status: res.status, count: ids.length, ids };
  } catch (err) {
    return { status: 0, count: 0, ids: [], error: String(err) };
  }
}

async function probe(model, prompt, maxTokens) {
  const t0 = Date.now();
  try {
    const res = await fetch(`${BASE}/chat/completions`, {
      method: 'POST',
      headers: headers(),
      body: JSON.stringify({
        model,
        messages: [{ role: 'user', content: prompt }],
        max_tokens: maxTokens,
      }),
    });
    const text = await res.text();
    const elapsed = Date.now() - t0;
    let body = null;
    try {
      body = JSON.parse(text);
    } catch {
      return { model, status: res.status, elapsed, ok: false, nonJson: text.slice(0, 120) };
    }
    if (body.error) {
      return { model, status: res.status, elapsed, ok: false, error: String(body.error.message || '').slice(0, 160) };
    }
    // A gateway that fails upstream can still answer with parseable JSON and no
    // `error` key -- crax returns exactly that on 502. Absent this check the probe
    // records `ok: true, usage: null, content: ""` for a model that answered
    // nothing, which is the opposite of what the report is for.
    const content = body.choices?.[0]?.message?.content || '';
    if (!res.ok || !content) {
      return {
        model,
        status: res.status,
        elapsed,
        ok: false,
        error: res.ok ? 'empty completion (no error field, no content)' : `HTTP ${res.status}, no error field in body`,
        usage: body.usage || null,
      };
    }
    return {
      model,
      status: res.status,
      elapsed,
      ok: true,
      usage: body.usage || null,
      upstreamModel: body.model || null,
      content: content.slice(0, 80),
    };
  } catch (err) {
    return { model, status: 0, elapsed: Date.now() - t0, ok: false, error: String(err).slice(0, 160) };
  }
}

async function main() {
  const report = { timestamp: new Date().toISOString(), base: BASE, models_endpoint: null, availability: [], usage_sanity: [] };

  console.log(`Probing ${BASE}`);
  report.models_endpoint = await listModels();
  console.log(`GET /models -> HTTP ${report.models_endpoint.status}, ${report.models_endpoint.count} ids`);
  if (report.models_endpoint.count) {
    console.log(report.models_endpoint.ids.slice(0, 60).join(', '));
  }

  console.log(`\n--- availability (1 tiny call each, ${GAP_MS}ms apart) ---`);
  for (const m of CANDIDATES) {
    const r = await probe(m, 'Reply with exactly: ok', 8);
    report.availability.push(r);
    const tag = r.ok ? `OK  in=${r.usage?.prompt_tokens} out=${r.usage?.completion_tokens} served=${r.upstreamModel}` : `${r.status} ${r.error || r.nonJson || ''}`;
    console.log(`  ${m.padEnd(22)} ${tag}`);
    await sleep(GAP_MS);
  }

  const alive = report.availability.filter((r) => r.ok).map((r) => r.model);
  console.log(`\nAlive models: ${alive.length ? alive.join(', ') : '(none)'}`);

  // Usage sanity: does prompt_tokens scale with prompt size, and is it stable?
  // A provider that returns the same prompt_tokens for 2 and 4000 tokens of input
  // is reporting a placeholder, and any token-savings figure taken from its usage
  // block is meaningless — which is a finding about the provider, not a result.
  if (alive.length) {
    const m = alive[0];
    console.log(`\n--- usage accounting sanity on ${m} ---`);
    const cases = [
      { label: 'tiny', prompt: 'ok' },
      { label: 'tiny-repeat', prompt: 'ok' },
      { label: 'padded-1k-words', prompt: 'Count the words. ' + 'lorem ipsum dolor sit amet '.repeat(200) },
      { label: 'padded-4k-words', prompt: 'Count the words. ' + 'lorem ipsum dolor sit amet '.repeat(800) },
    ];
    for (const c of cases) {
      const r = await probe(m, c.prompt, 8);
      const chars = c.prompt.length;
      report.usage_sanity.push({ ...c, prompt_chars: chars, prompt: undefined, result: r });
      console.log(
        `  ${c.label.padEnd(16)} chars=${String(chars).padEnd(6)} -> in=${r.usage?.prompt_tokens} out=${r.usage?.completion_tokens} (${r.ok ? 'ok' : r.status + ' ' + (r.error || '')})`
      );
      await sleep(GAP_MS);
    }
  }

  const outDir = 'test/results';
  fs.mkdirSync(outDir, { recursive: true });
  const out = path.join(outDir, OUT_NAME);
  fs.writeFileSync(out, JSON.stringify(report, null, 2), 'utf-8');
  console.log(`\nSaved ${out}`);
}

main();
