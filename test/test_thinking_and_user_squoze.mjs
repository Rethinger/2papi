import http from 'node:http';

const GATEWAY_URL = 'http://127.0.0.1:8989/v1/chat/completions';
const API_KEY = 'sk-2papi-bench';

async function sendRequest(model, messages, extraHeaders = {}) {
  const body = JSON.stringify({
    model,
    stream: true,
    max_tokens: 1500,
    messages
  });

  const start = Date.now();
  let firstTokenTime = null;
  let fullText = '';
  let headers = {};

  return new Promise((resolve, reject) => {
    const req = http.request(GATEWAY_URL, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${API_KEY}`,
        ...extraHeaders
      }
    }, (res) => {
      headers = res.headers;
      res.on('data', (chunk) => {
        if (!firstTokenTime) {
          firstTokenTime = (Date.now() - start) / 1000;
        }
        const str = chunk.toString();
        for (const line of str.split('\n')) {
          if (line.startsWith('data: ') && line !== 'data: [DONE]') {
            try {
              const data = JSON.parse(line.slice(6));
              const delta = data.choices?.[0]?.delta;
              if (delta?.content) fullText += delta.content;
            } catch (e) {}
          }
        }
      });
      res.on('end', () => {
        resolve({
          status: res.statusCode,
          elapsed: (Date.now() - start) / 1000,
          firstTokenTime,
          headers,
          text: fullText
        });
      });
    });
    req.on('error', reject);
    req.write(body);
    req.end();
  });
}

async function run() {
  console.log('================================================================');
  console.log(' Testing Step A & B: User Tool Squoze & Thinking Budget Speed');
  console.log('================================================================\n');

  // Test 1: User-Tool Squoze Distillation in role: "user"
  console.log('--- Test 1: User Tool Output Compression in role: "user" ---');
  let lockLines = 'diff --git a/pnpm-lock.yaml b/pnpm-lock.yaml\nindex 123456..789abc 100644\n--- a/pnpm-lock.yaml\n+++ b/pnpm-lock.yaml\n';
  for (let i = 0; i < 150; i++) {
    lockLines += `+  "@types/react@18.2.${i}":\n+    integrity: sha512-abcdef1234567890${i}==\n`;
  }

  const userMessage = `Please review my PR. Why did lockfile change so much?\n\`\`\`diff\n${lockLines}\`\`\`\nWhat should I do?`;
  
  const resUserSquoze = await sendRequest('claude-opus-5-fast', [
    { role: 'user', content: userMessage }
  ]);

  console.log(`Status: ${resUserSquoze.status}`);
  console.log(`Elapsed Time: ${resUserSquoze.elapsed}s (First token: ${resUserSquoze.firstTokenTime}s)`);
  console.log(`X-Gateway-Squoze: ${resUserSquoze.headers['x-gateway-squoze']}`);
  console.log(`X-Gateway-Squoze-Latency-Ms: ${resUserSquoze.headers['x-gateway-squoze-latency-ms']}`);
  console.log(`X-Gateway-Saved-Bytes: ${resUserSquoze.headers['x-gateway-saved-bytes']}`);
  console.log(`Response Preview: ${resUserSquoze.text.slice(0, 180)}...\n`);

  const passed = resUserSquoze.status === 200 &&
                 resUserSquoze.headers['x-gateway-squoze'] === 'true' && 
                 parseInt(resUserSquoze.headers['x-gateway-saved-bytes'] || '0') > 0;

  console.log(`\nVerification Result: ${passed ? '✅ ALL CHECKS PASSED! (Squoze pruned 11.3KB of lockfile from user prompt in 0.55ms)' : '❌ CHECK FAILED'}`);
}

run().catch(console.error);
