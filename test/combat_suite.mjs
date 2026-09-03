import fs from 'fs';
import path from 'path';

const SCENARIOS = [
  'test/benchmarks/scenario1_deadlock.json',
  'test/benchmarks/scenario2_monorepo.json',
  'test/benchmarks/scenario3_multiturn.json'
];

const RESULTS_DIR = 'test/results';
if (!fs.existsSync(RESULTS_DIR)) {
  fs.mkdirSync(RESULTS_DIR, { recursive: true });
}

async function executeScenario(scenarioPath, modelAlias, label) {
  const scenarioData = JSON.parse(fs.readFileSync(scenarioPath, 'utf-8'));
  console.log(`\n------------------------------------------------------------`);
  console.log(` Running [${scenarioData.name}]`);
  console.log(` Configuration: ${label} (Model: ${modelAlias})`);
  console.log(`------------------------------------------------------------`);

  const messages = [];
  if (scenarioData.system_instruction) {
    messages.push({ role: 'system', content: scenarioData.system_instruction });
  }
  messages.push(...scenarioData.messages);

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
      console.error(`Request failed with status ${res.status}: ${err}`);
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
    const groundTruth = scenarioData.ground_truth;
    const lowerResponse = fullResponse.toLowerCase();
    
    let matchedKeywords = 0;
    for (const kw of groundTruth.root_cause_keywords) {
      if (lowerResponse.includes(kw.toLowerCase())) matchedKeywords++;
    }

    let matchedPatterns = 0;
    for (const pat of groundTruth.patterns) {
      if (fullResponse.includes(pat) || lowerResponse.includes(pat.toLowerCase())) matchedPatterns++;
    }

    const accuracyScore = {
      keywordMatch: `${matchedKeywords}/${groundTruth.root_cause_keywords.length}`,
      patternMatch: `${matchedPatterns}/${groundTruth.patterns.length}`,
      verified: matchedKeywords >= 1 && matchedPatterns >= 1
    };

    console.log(`Telemetry:`);
    console.log(`  - Squoze Active: ${headers.squoze}`);
    console.log(`  - Squoze Overhead: ${headers.squozeLatencyMs.toFixed(2)} ms`);
    console.log(`  - Saved Bytes: ${headers.savedBytes}`);
    if (usage) {
      console.log(`  - Tokens: Input=${usage.prompt_tokens} | Output=${usage.completion_tokens} | Total=${usage.total_tokens}`);
    }
    console.log(`  - Accuracy: ${accuracyScore.verified ? 'PASSED' : 'CHECK'} (Keywords: ${accuracyScore.keywordMatch}, Patterns: ${accuracyScore.patternMatch})`);

    return {
      success: true,
      label,
      modelAlias,
      elapsed,
      firstTokenTime,
      headers,
      usage,
      accuracyScore,
      responsePreview: fullResponse.slice(0, 300) + '...'
    };
  } catch (err) {
    console.error(`Scenario execution failed:`, err);
    return { success: false, error: String(err), headers };
  }
}

async function main() {
  console.log(`================================================================`);
  console.log(` Squoze v2 vs Baseline: Production Combat Benchmark Suite`);
  console.log(` Upstream Provider: gorouter.app | Gateway: 2papi (:8989)`);
  console.log(` Target Model: Claude Opus 5`);
  console.log(`================================================================`);

  const fullReport = {
    timestamp: new Date().toISOString(),
    suite: 'Squoze v2 Real-World Engineering Scenarios',
    provider: 'gorouter.app',
    model: 'claude-opus-5',
    scenarios: []
  };

  for (let i = 0; i < SCENARIOS.length; i++) {
    const scPath = SCENARIOS[i];
    const scJson = JSON.parse(fs.readFileSync(scPath, 'utf-8'));
    console.log(`\n============================================================`);
    console.log(` >>> Executing Test Suite ${i + 1}/${SCENARIOS.length}: ${scJson.name}`);
    console.log(`============================================================`);

    // 1. With Squoze v2
    const withSquoze = await executeScenario(scPath, 'claude-opus-5', 'With Squoze v2');

    console.log(`\nWaiting 5 seconds between runs...`);
    await new Promise(r => setTimeout(r, 5000));

    // 2. Without Squoze (Baseline)
    const withoutSquoze = await executeScenario(scPath, 'claude-opus-5-nosquoze', 'Without Squoze (Baseline)');

    fullReport.scenarios.push({
      id: `SCENARIO-${i + 1}`,
      name: scJson.name,
      with_squoze: withSquoze,
      without_squoze: withoutSquoze
    });

    if (i < SCENARIOS.length - 1) {
      console.log(`\nWaiting 8 seconds before next scenario...`);
      await new Promise(r => setTimeout(r, 8000));
    }
  }

  const outPath = path.join(RESULTS_DIR, 'combat_suite_report.json');
  fs.writeFileSync(outPath, JSON.stringify(fullReport, null, 2), 'utf-8');

  console.log(`\n================================================================`);
  console.log(` Combat Benchmark Suite Complete!`);
  console.log(` Detailed Report Saved To: ${outPath}`);
  console.log(`================================================================\n`);
}

main();
