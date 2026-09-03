import fs from 'fs';
import path from 'path';

const LIVE_TASKS = [
  'test/benchmarks/live_issue_chi_641.json'
];

const RESULTS_DIR = 'test/results';
if (!fs.existsSync(RESULTS_DIR)) {
  fs.mkdirSync(RESULTS_DIR, { recursive: true });
}

async function runLiveIssueTask(taskPath, modelAlias, label) {
  const task = JSON.parse(fs.readFileSync(taskPath, 'utf-8'));
  console.log(`\n------------------------------------------------------------`);
  console.log(` Live Open Issue: [${task.instance_id}] (${task.repo})`);
  console.log(` Title: ${task.issue_title}`);
  console.log(` Runner: ${label} (Model: ${modelAlias})`);
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
        max_tokens: 4000,
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
    for (const pat of gt.required_patterns) {
      if (fullResponse.toLowerCase().includes(pat.toLowerCase())) matchedPatterns++;
    }

    const passed = matchedPatterns >= Math.ceil(gt.required_patterns.length * 0.66);
    const hasDiff = fullResponse.includes('diff --git') || fullResponse.includes('--- a/') || fullResponse.includes('@@');

    console.log(`Evaluation:`);
    console.log(`  - Squoze Active: ${headers.squoze}`);
    console.log(`  - Squoze Latency: ${headers.squozeLatencyMs.toFixed(2)} ms`);
    console.log(`  - Saved Bytes: ${headers.savedBytes}`);
    if (usage) {
      console.log(`  - Tokens: Input=${usage.prompt_tokens} | Output=${usage.completion_tokens} | Total=${usage.total_tokens}`);
    }
    console.log(`  - Patch Format Valid: ${hasDiff ? 'YES' : 'NO'}`);
    console.log(`  - Verification: ${passed ? 'PASSED' : 'FAILED'} (Matched: ${matchedPatterns}/${gt.required_patterns.length})`);

    return {
      success: true,
      instance_id: task.instance_id,
      repo: task.repo,
      issue_url: task.issue_url,
      label,
      modelAlias,
      elapsed,
      firstTokenTime,
      headers,
      usage,
      passed,
      hasDiff,
      matchedPatterns: `${matchedPatterns}/${gt.required_patterns.length}`,
      patchPreview: fullResponse.slice(0, 400) + '...',
      fullPatch: fullResponse
    };
  } catch (err) {
    console.error(`Task execution error:`, err);
    return { success: false, error: String(err), headers };
  }
}

async function main() {
  console.log(`================================================================`);
  console.log(` Live Open-Source Bug Hunter & Benchmark Suite`);
  console.log(` Target: Active Unresolved GitHub Issues (Textualize/rich, go-chi/chi)`);
  console.log(` Framework: 2papi (Squoze v2) -> gorouter.app (Claude Opus 5)`);
  console.log(`================================================================`);

  const report = {
    timestamp: new Date().toISOString(),
    benchmark: 'Live OSS Issue Benchmark',
    provider: 'gorouter.app',
    model: 'claude-opus-5',
    results: []
  };

  for (let i = 0; i < LIVE_TASKS.length; i++) {
    const taskPath = LIVE_TASKS[i];
    const taskJson = JSON.parse(fs.readFileSync(taskPath, 'utf-8'));
    console.log(`\n============================================================`);
    console.log(` >>> Hunting Live Issue ${i + 1}/${LIVE_TASKS.length}: [${taskJson.instance_id}]`);
    console.log(`============================================================`);

    // 1. With Squoze v2
    const withSquoze = await runLiveIssueTask(taskPath, 'claude-opus-5', 'With Squoze v2');

    console.log(`\nWaiting 5 seconds between runs...`);
    await new Promise(r => setTimeout(r, 5000));

    // 2. Without Squoze (Baseline)
    const withoutSquoze = await runLiveIssueTask(taskPath, 'claude-opus-5-nosquoze', 'Without Squoze (Baseline)');

    report.results.push({
      instance_id: taskJson.instance_id,
      repo: taskJson.repo,
      issue_url: taskJson.issue_url,
      issue_title: taskJson.issue_title,
      with_squoze: withSquoze,
      without_squoze: withoutSquoze
    });

    if (i < LIVE_TASKS.length - 1) {
      console.log(`\nWaiting 8 seconds before next issue...`);
      await new Promise(r => setTimeout(r, 8000));
    }
  }

  const outPath = path.join(RESULTS_DIR, 'live_oss_report.json');
  fs.writeFileSync(outPath, JSON.stringify(report, null, 2), 'utf-8');

  console.log(`\n================================================================`);
  console.log(` Live Open-Source Bug Hunt Complete!`);
  console.log(` Report Saved To: ${outPath}`);
  console.log(`================================================================\n`);
}

main();
