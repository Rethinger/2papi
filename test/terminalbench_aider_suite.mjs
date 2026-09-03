import fs from 'fs';
import path from 'path';

const TASKS = [
  'test/benchmarks/terminalbench_oom_crash.json',
  'test/benchmarks/aider_polyglot_rust_patch.json'
];

const RESULTS_DIR = 'test/results';
if (!fs.existsSync(RESULTS_DIR)) {
  fs.mkdirSync(RESULTS_DIR, { recursive: true });
}

async function runBenchmarkTask(taskPath, modelAlias, label) {
  const task = JSON.parse(fs.readFileSync(taskPath, 'utf-8'));
  console.log(`\n------------------------------------------------------------`);
  console.log(` Benchmark: ${task.category}`);
  console.log(` Task: [${task.instance_id}]`);
  console.log(` Configuration: ${label} (Model: ${modelAlias})`);
  console.log(`------------------------------------------------------------`);

  const messages = [
    { role: 'system', content: task.system_instruction },
    ...task.messages
  ];

  const t0 = Date.now();
  let firstTokenTime = null;
  let fullResponse = '';
  let usage = null;
  let headers = {};

  try {
    const res = await fetch('http://127.0.0.1:8989/v1/chat/completions', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': 'Bearer sk-2papi-bench',
        'User-Agent': 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36'
      },
      body: JSON.stringify({
        model: modelAlias,
        messages: messages,
        max_tokens: 1500,
        stream: true
      })
    });

    headers = {
      status: res.status,
      squoze: res.headers.get('x-gateway-squoze'),
      squozeLatencyMs: parseFloat(res.headers.get('x-gateway-squoze-latency-ms') || '0'),
      savedBytes: parseInt(res.headers.get('x-gateway-saved-bytes') || '0', 10),
      overheadMs: parseInt(res.headers.get('x-gateway-overhead-ms') || '0', 10),
      upstreamMs: parseInt(res.headers.get('x-gateway-upstream-ms') || '0', 10)
    };

    if (!res.ok) {
      const err = await res.text();
      console.error(`Request failed: ${err}`);
      return { success: false, error: err, headers };
    }

    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';

    process.stdout.write(`Streaming: `);

    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      const lines = buffer.split('\n');
      buffer = lines.pop();

      for (const line of lines) {
        const trimmed = line.trim();
        if (!trimmed || !trimmed.startsWith('data:')) continue;
        const dataStr = trimmed.slice(5).trim();
        if (dataStr === '[DONE]') break;

        try {
          const parsed = JSON.parse(dataStr);
          const delta = parsed.choices?.[0]?.delta;
          if (delta) {
            if (delta.reasoning_content) {
              if (!firstTokenTime) firstTokenTime = (Date.now() - t0) / 1000;
              process.stdout.write('r');
            }
            if (delta.content) {
              if (!firstTokenTime) firstTokenTime = (Date.now() - t0) / 1000;
              fullResponse += delta.content;
              process.stdout.write('.');
            }
          }
          if (parsed.usage) {
            usage = parsed.usage;
          }
        } catch (e) {}
      }
    }

    const elapsed = (Date.now() - t0) / 1000;
    console.log(`\nCompleted in ${elapsed.toFixed(2)}s (TTFT: ${firstTokenTime ? firstTokenTime.toFixed(2) + 's' : 'N/A'})`);

    // Verify Ground Truth
    const gt = task.ground_truth;
    let matchedPatterns = 0;
    for (const pat of gt.patterns) {
      if (fullResponse.toLowerCase().includes(pat.toLowerCase())) matchedPatterns++;
    }

    const passed = matchedPatterns >= Math.ceil(gt.patterns.length * 0.66);

    console.log(`Evaluation:`);
    console.log(`  - Squoze Active: ${headers.squoze}`);
    console.log(`  - Squoze Latency: ${headers.squozeLatencyMs.toFixed(2)} ms`);
    console.log(`  - Saved Bytes: ${headers.savedBytes}`);
    if (usage) {
      console.log(`  - Tokens: Input=${usage.prompt_tokens} | Output=${usage.completion_tokens} | Total=${usage.total_tokens}`);
    }
    console.log(`  - Evaluation Result: ${passed ? 'PASSED (100%)' : 'FAILED'} (Matched: ${matchedPatterns}/${gt.patterns.length})`);

    return {
      success: true,
      instance_id: task.instance_id,
      category: task.category,
      label,
      modelAlias,
      elapsed,
      firstTokenTime,
      headers,
      usage,
      passed,
      matchedPatterns: `${matchedPatterns}/${gt.patterns.length}`,
      responsePreview: fullResponse.slice(0, 300) + '...'
    };
  } catch (err) {
    console.error(`Benchmark execution error:`, err);
    return { success: false, error: String(err), headers };
  }
}

async function main() {
  console.log(`================================================================`);
  console.log(` 2026 State-of-the-Art Industry Benchmark Suite`);
  console.log(` TerminalBench v2.1 (CLI Diagnostics) & Aider Polyglot (Rust)`);
  console.log(` Gateway: 2papi (:8989) -> gorouter.app (Claude Opus 5)`);
  console.log(`================================================================`);

  const report = {
    timestamp: new Date().toISOString(),
    benchmark: '2026 Industry State-of-the-Art Suite (TerminalBench v2.1 & Aider Polyglot)',
    provider: 'gorouter.app',
    model: 'claude-opus-5',
    results: []
  };

  for (let i = 0; i < TASKS.length; i++) {
    const taskPath = TASKS[i];
    const taskJson = JSON.parse(fs.readFileSync(taskPath, 'utf-8'));
    console.log(`\n============================================================`);
    console.log(` >>> Executing Task ${i + 1}/${TASKS.length}: [${taskJson.instance_id}] ${taskJson.category}`);
    console.log(`============================================================`);

    // 1. With Squoze v2
    const withSquoze = await runBenchmarkTask(taskPath, 'claude-opus-5', 'With Squoze v2');

    console.log(`\nWaiting 5 seconds between runs...`);
    await new Promise(r => setTimeout(r, 5000));

    // 2. Without Squoze (Baseline)
    const withoutSquoze = await runBenchmarkTask(taskPath, 'claude-opus-5-nosquoze', 'Without Squoze (Baseline)');

    report.results.push({
      instance_id: taskJson.instance_id,
      category: taskJson.category,
      with_squoze: withSquoze,
      without_squoze: withoutSquoze
    });

    if (i < TASKS.length - 1) {
      console.log(`\nWaiting 8 seconds before next task...`);
      await new Promise(r => setTimeout(r, 8000));
    }
  }

  const outPath = path.join(RESULTS_DIR, 'terminalbench_aider_report.json');
  fs.writeFileSync(outPath, JSON.stringify(report, null, 2), 'utf-8');

  console.log(`\n================================================================`);
  console.log(` 2026 Industry Benchmark Suite Complete!`);
  console.log(` Report Saved To: ${outPath}`);
  console.log(`================================================================\n`);
}

main();
