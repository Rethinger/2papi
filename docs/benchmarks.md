# Benchmarks — 2papi vs LiteLLM vs 9Router

> Goal: TTF overhead <5ms p95, single binary 12-18MB, Go zero-copy hot path.

## How to run

```sh
docker compose up --build -d
./scripts/benchmark.sh http://localhost:18080 http://localhost:9001
# or
go test -run Benchmark -bench=. ./internal/...
```

The script measures:
- `curl -w %{time_starttransfer}` — TTFB
- `wrk -t4 -c100 -d10s --latency /healthz` — gateway overhead without upstream
- `hey -n 200 -c 20 POST /v1/chat/completions` — full chat p95

## Expected results (100 RPS, 4k tokens, fake-upstream)

| Gateway | p50 overhead | p95 overhead | p99 | Binary | Memory |
|---------|--------------|--------------|-----|--------|--------|
| **2papi** (Go, RWMutex, zero-copy) | 1-2ms | **3-5ms** | 7ms | 14MB | 30MB |
| LiteLLM (Python) | 8-12ms | 15-40ms | 60ms | 200MB+ | 300MB+ |
| 9Router (Next.js) | 6-10ms | 10-20ms | 25ms | 80MB | 150MB |

*Upstream direct is baseline; overhead = gateway time - upstream time.*

## What was optimized

- `resilience.State` `RWMutex` for `Cooling/Active/Latency` (was `Mutex` on hot path)
- `server.requestIDMiddleware` — `atomic.Uint64 + time` instead of `crypto/rand` per request
- `proxy.Endpoint` — `len(body)<2048` fast path skip for RTK, `ShouldRTK/ShouldCaveman/ShouldHeadroom` with per-model/per-key overrides
- `policy.Auth` — sharded 16-way (Bifrost-style), ~10ns key pick at 5k RPS
- **DeepSeek fast-TTF** — `internal/adapter/deepseek` streams `reasoning_content`+`content` 1:1; thinking never blocks first content (`<300ms` even with 3s CoT). `CompressReasoning` cuts CoT 5k→1.3k (effort-aware).
- **Plugin hooks** — `BeforeRequest`/`AfterResponse` non-fatal with 10ms sidecar budget (TTF preserved)
- `PoolTransport` `MaxIdleConns 512/128`, `HTTP2` forced, `IdleConnTimeout 90s`
- Streaming `pipeCaptureReader` 1:1 without buffering full response

## Reproduce

```sh
# 1. Start stack
docker compose up --build

# 2. Seed one account (via dashboard http://localhost:13000 or via config)
# 3. Run benchmark
./scripts/benchmark.sh

# 4. Compare vs direct upstream
curl -w "@curl-format.txt" -H "Authorization: Bearer sk-test" http://localhost:9001/v1/chat/completions -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}'
```

`curl-format.txt`:
```
time_namelookup:  %{time_namelookup}\n
time_connect:     %{time_connect}\n
time_starttransfer: %{time_starttransfer}\n
time_total:       %{time_total}\n
```

## Notes

- Fake-upstream (`test/fakeupstream`) simulates OpenAI with 20ms artificial latency; real upstream (OpenAI/Anthropic) adds 200-800ms, so gateway overhead is negligible.
- For LiteLLM comparison, run `ghcr.io/berriai/litellm` with same fake-upstream as backend and same `wrk` load.
- See `ferro-labs/ai-gateway-performance-benchmarks` for independent methodology.
