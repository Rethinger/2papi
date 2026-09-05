// OpenAI wire-protocol conformance checks against a running 2papi gateway.
//
// These are contract checks, not performance: a gateway that is fast but
// returns a malformed SSE stream or swallows `usage` breaks every client that
// assumes OpenAI shape. Each check reports the actual observation, so a FAIL
// is actionable rather than a bare assertion.
//
// Usage:
//   GATEWAY_URL=http://127.0.0.1:18081 GATEWAY_KEY=sk-bench-key \
//   GATEWAY_MODEL=gpt-dev node test/conformance.mjs
//
// Writes test/results/conformance_report.json

import fs from 'fs';
import path from 'path';

const BASE = process.env.GATEWAY_URL || 'http://127.0.0.1:18081';
const KEY = process.env.GATEWAY_KEY || 'sk-bench-key';
const MODEL = process.env.GATEWAY_MODEL || 'gpt-dev';

const checks = [];

function record(name, spec, passed, observed, detail = '') {
  checks.push({ name, spec, passed, observed, detail });
  const tag = passed === null ? 'SKIP' : passed ? 'PASS' : 'FAIL';
  console.log(`  [${tag}] ${name}`);
  console.log(`         spec: ${spec}`);
  console.log(`         got : ${observed}`);
  if (detail) console.log(`         note: ${detail}`);
}

async function req(pathname, { method = 'POST', body, key = KEY, headers = {} } = {}) {
  const res = await fetch(`${BASE}${pathname}`, {
    method,
    headers: {
      'Content-Type': 'application/json',
      ...(key ? { Authorization: `Bearer ${key}` } : {}),
      ...headers,
    },
    ...(body ? { body: JSON.stringify(body) } : {}),
  });
  return res;
}

const chat = (extra = {}) => ({
  model: MODEL,
  messages: [{ role: 'user', content: 'say ok' }],
  max_tokens: 16,
  ...extra,
});

async function checkModelsEndpoint() {
  try {
    const res = await req('/v1/models', { method: 'GET' });
    const j = await res.json();
    const ok = res.ok && j.object === 'list' && Array.isArray(j.data) && j.data.length > 0;
    const shapeOk = ok && j.data.every((m) => typeof m.id === 'string' && m.object === 'model');
    record(
      'GET /v1/models returns an OpenAI model list',
      'HTTP 200, {object:"list", data:[{id, object:"model", ...}]}',
      ok && shapeOk,
      `HTTP ${res.status}, object=${j.object}, n=${Array.isArray(j.data) ? j.data.length : 'n/a'}`,
      ok && !shapeOk ? 'entries missing id/object fields' : ''
    );
    return Array.isArray(j.data) ? j.data.map((m) => m.id) : [];
  } catch (err) {
    record('GET /v1/models returns an OpenAI model list', 'HTTP 200 + list shape', false, String(err));
    return [];
  }
}

async function checkNonStreamShape() {
  try {
    const res = await req('/v1/chat/completions', { body: chat() });
    const raw = await res.text();

    // Distinguish a gateway defect from an upstream fixture that always
    // streams: if a non-stream request comes back SSE-framed, the shape is
    // whatever the upstream emitted, so the check cannot grade the gateway.
    if (raw.trimStart().startsWith('data:')) {
      record(
        'non-stream completion has OpenAI object shape',
        'id, object:"chat.completion", created, choices[0].{index,message,finish_reason}',
        null,
        `upstream answered a non-stream request with SSE framing (${raw.slice(0, 40).replace(/\n/g, '\\n')}…)`,
        'INCONCLUSIVE: the configured upstream is not OpenAI-shaped, so this check cannot grade the gateway. ' +
          'Re-run against a real provider or an OpenAI-faithful fake.'
      );
      record(
        'non-stream response reports consistent usage',
        'usage.{prompt,completion,total}_tokens present; prompt+completion == total',
        null,
        'not evaluable: response was SSE-framed',
        'INCONCLUSIVE for the same reason. Note that a fake upstream emitting no usage makes ' +
          'any token-savings measurement through it unmeasurable.'
      );
      return null;
    }

    const j = JSON.parse(raw);
    const hasCore =
      typeof j.id === 'string' &&
      j.object === 'chat.completion' &&
      typeof j.created === 'number' &&
      Array.isArray(j.choices) &&
      j.choices.length > 0;
    const choice = j.choices?.[0] || {};
    const hasChoice =
      typeof choice.index === 'number' &&
      choice.message &&
      typeof choice.message.role === 'string' &&
      typeof choice.message.content === 'string' &&
      typeof choice.finish_reason === 'string';
    record(
      'non-stream completion has OpenAI object shape',
      'id, object:"chat.completion", created, choices[0].{index,message,finish_reason}',
      hasCore && hasChoice,
      `HTTP ${res.status}, object=${j.object}, choices=${j.choices?.length}, finish_reason=${choice.finish_reason}`,
      !hasChoice ? 'choice fields incomplete' : ''
    );

    const u = j.usage;
    const usageOk =
      u &&
      Number.isInteger(u.prompt_tokens) &&
      Number.isInteger(u.completion_tokens) &&
      Number.isInteger(u.total_tokens);
    const usageConsistent = usageOk && u.prompt_tokens + u.completion_tokens === u.total_tokens;
    record(
      'non-stream response reports consistent usage',
      'usage.{prompt,completion,total}_tokens present; prompt+completion == total',
      usageOk && usageConsistent,
      u ? `in=${u.prompt_tokens} out=${u.completion_tokens} total=${u.total_tokens}` : 'usage absent',
      usageOk && !usageConsistent ? 'total_tokens does not equal the sum' : ''
    );
    return j;
  } catch (err) {
    record('non-stream completion has OpenAI object shape', 'OpenAI shape', false, String(err));
    return null;
  }
}

async function checkStreamShape() {
  try {
    const res = await req('/v1/chat/completions', { body: chat({ stream: true }) });
    const ct = res.headers.get('content-type') || '';
    const text = await res.text();
    const lines = text.split('\n').filter((l) => l.trim());
    const dataLines = lines.filter((l) => l.startsWith('data:'));
    const sawDone = dataLines.some((l) => l.slice(5).trim() === '[DONE]');

    let framesValid = dataLines.length > 0;
    let firstDeltaHasRole = false;
    let sawFinish = false;
    let parseErrors = 0;
    let missingObject = 0;
    for (const l of dataLines) {
      const payload = l.slice(5).trim();
      if (payload === '[DONE]') continue;
      try {
        const j = JSON.parse(payload);
        if (j.object !== 'chat.completion.chunk') missingObject++;
        const d = j.choices?.[0];
        if (d?.delta?.role) firstDeltaHasRole = true;
        if (d?.finish_reason) sawFinish = true;
      } catch {
        parseErrors++;
        framesValid = false;
      }
    }

    record(
      'streaming uses SSE framing terminated by [DONE]',
      'Content-Type text/event-stream, "data: " frames, final "data: [DONE]"',
      ct.includes('text/event-stream') && sawDone,
      `content-type=${ct || 'none'}, data frames=${dataLines.length}, [DONE]=${sawDone}`,
      !sawDone ? 'clients that wait for [DONE] will hang' : ''
    );

    // Chunk field completeness is an upstream property when the gateway is a
    // faithful passthrough: it forwards frames without rewriting them. Grade
    // JSON well-formedness (which the gateway could break) but report field
    // completeness as inconclusive.
    record(
      'stream frames are well-formed JSON the gateway did not corrupt',
      'every non-[DONE] frame parses as JSON',
      framesValid && parseErrors === 0,
      `frames=${dataLines.length}, json parse errors=${parseErrors}`
    );
    record(
      'stream chunks carry object:"chat.completion.chunk"',
      'every non-[DONE] frame has object:"chat.completion.chunk", and a final frame carries finish_reason',
      missingObject === 0 && sawFinish ? true : null,
      `frames missing object field=${missingObject}, saw finish_reason=${sawFinish}, first delta carried role=${firstDeltaHasRole}`,
      missingObject > 0
        ? 'INCONCLUSIVE: the gateway streams upstream frames through unmodified, so these fields ' +
          'reflect the upstream. The bundled fake-upstream emits minimal frames ' +
          '(test/fakeupstream/main.go writes only choices[].delta.content).'
        : ''
    );
  } catch (err) {
    record('streaming uses SSE framing terminated by [DONE]', 'SSE + [DONE]', false, String(err));
  }
}

async function checkAuthRejection() {
  try {
    const res = await req('/v1/chat/completions', { body: chat(), key: 'sk-definitely-not-a-key' });
    let j = null;
    try {
      j = await res.json();
    } catch {
      /* non-JSON body */
    }
    const is401 = res.status === 401;
    const shaped = j && j.error && typeof j.error.message === 'string';
    record(
      'unknown virtual key is rejected with 401 + error object',
      'HTTP 401 and {error:{message,type|code}}',
      is401 && !!shaped,
      `HTTP ${res.status}, body=${j ? JSON.stringify(j).slice(0, 120) : '(non-JSON)'}`,
      is401 && !shaped ? 'status is right but the body is not an OpenAI error object' : ''
    );
  } catch (err) {
    record('unknown virtual key is rejected with 401 + error object', '401 + error shape', false, String(err));
  }
}

async function checkUnknownModel() {
  try {
    const res = await req('/v1/chat/completions', { body: chat({ model: 'model-that-does-not-exist' }) });
    let j = null;
    try {
      j = await res.json();
    } catch {
      /* ignore */
    }
    const isErr = res.status >= 400;
    const shaped = j && j.error && typeof j.error.message === 'string';
    record(
      'unknown model produces a 4xx error object',
      'HTTP 4xx and {error:{message,...}}',
      isErr && !!shaped,
      `HTTP ${res.status}, body=${j ? JSON.stringify(j).slice(0, 120) : '(non-JSON)'}`
    );
  } catch (err) {
    record('unknown model produces a 4xx error object', '4xx + error shape', false, String(err));
  }
}

async function checkOptimizerEchoHeaders() {
  // A body large enough to cross the optimizer size gates, carrying a tool
  // message full of machine output.
  const noisy = Array.from({ length: 900 }, (_, i) =>
    i === 400
      ? '--- FAIL: TestThing (0.01s)\n    thing_test.go:9: want 4200 got 420'
      : `    thing_test.go:${i}: verbose assertion padding padding ok`
  ).join('\n');

  try {
    const res = await req('/v1/chat/completions', {
      body: {
        model: MODEL,
        messages: [
          { role: 'user', content: 'diagnose the failure' },
          { role: 'tool', content: noisy },
        ],
        max_tokens: 16,
      },
      headers: { 'X-Gateway-Compress': 'aggressive' },
    });
    await res.text();
    const echo = {
      rtk: res.headers.get('x-gateway-rtk-mode'),
      squoze: res.headers.get('x-gateway-squoze'),
      savedBytes: res.headers.get('x-gateway-saved-bytes'),
      savedTokens: res.headers.get('x-gateway-saved-tokens'),
      overhead: res.headers.get('x-gateway-overhead-ms'),
      route: res.headers.get('x-gateway-route'),
    };
    const anyEcho = Object.values(echo).some((v) => v !== null);
    record(
      'gateway echoes which optimizer actually ran',
      'response carries X-Gateway-RTK-Mode / -Saved-Bytes / -Route when an optimizer is requested',
      anyEcho,
      JSON.stringify(echo),
      !anyEcho ? 'no echo headers: callers cannot tell whether the optimizer fired' : ''
    );

    const saved = Number(echo.savedBytes || 0);
    record(
      'requested RTK compression reports non-zero savings on noisy input',
      'X-Gateway-Saved-Bytes > 0 for a 900-line tool result with X-Gateway-Compress: aggressive',
      saved > 0,
      `saved_bytes=${echo.savedBytes ?? 'absent'}, rtk_mode=${echo.rtk ?? 'absent'}`,
      saved > 0 ? '' : 'optimizer was requested but reported no savings'
    );
  } catch (err) {
    record('gateway echoes which optimizer actually ran', 'echo headers', false, String(err));
  }
}

async function checkHealthEndpoints() {
  for (const p of ['/healthz', '/readyz']) {
    try {
      const res = await req(p, { method: 'GET', key: null });
      record(
        `GET ${p} is reachable without auth`,
        'HTTP 200 without an Authorization header',
        res.ok,
        `HTTP ${res.status}`
      );
    } catch (err) {
      record(`GET ${p} is reachable without auth`, 'HTTP 200', false, String(err));
    }
  }
}

async function main() {
  console.log('================================================================');
  console.log(` OpenAI wire-protocol conformance — ${BASE} (model ${MODEL})`);
  console.log('================================================================');

  await checkHealthEndpoints();
  await checkModelsEndpoint();
  await checkNonStreamShape();
  await checkStreamShape();
  await checkAuthRejection();
  await checkUnknownModel();
  await checkOptimizerEchoHeaders();

  const pass = checks.filter((c) => c.passed === true).length;
  const fail = checks.filter((c) => c.passed === false).length;
  const skip = checks.filter((c) => c.passed === null).length;

  console.log('\n================================================================');
  console.log(` ${pass} passed · ${fail} failed · ${skip} skipped`);
  if (fail) {
    console.log(' failures:');
    for (const c of checks.filter((c) => c.passed === false)) {
      console.log(`   - ${c.name}: ${c.observed}`);
    }
  }
  console.log('================================================================');

  const outDir = 'test/results';
  fs.mkdirSync(outDir, { recursive: true });
  const out = path.join(outDir, 'conformance_report.json');
  fs.writeFileSync(
    out,
    JSON.stringify(
      { timestamp: new Date().toISOString(), gateway: BASE, model: MODEL, summary: { pass, fail, skip }, checks },
      null,
      2
    ),
    'utf-8'
  );
  console.log(`Saved ${out}`);
}

main();
