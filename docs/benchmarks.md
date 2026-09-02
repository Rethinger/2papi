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

## Optimization-mode matrix (виток 8) — measured 2026-09-02

Unlike the table above (targets), these are measured numbers. Harness:
`test/bench.mjs` with `BENCH_MATRIX=1`, one warm gateway process, fake-upstream,
20 concurrent, 6s per mode, 14 modes × 3 payload profiles.

```sh
docker compose --profile bench up -d --build fake-upstream gateway-bench
docker compose --profile bench run --rm bench-matrix
```

`ovh` = gateway self-reported `X-Gateway-Overhead-Ms` (its own work, upstream
excluded). Δ is against the baseline row of the same payload profile. `applied`
comes from the echo headers, so a mode that decided to do nothing is visible as
such rather than being credited with the work.

Payload profiles exist because every optimizer is size-gated — a single tiny
body would report "no overhead" for all 14 modes and prove nothing:

| profile | size | what it exercises |
|---|---|---|
| `small` | 0.1 KiB | pure gateway overhead; every optimizer short-circuits |
| `large` | 96.9 KiB | RTK and caveman do real work; headroom stays under reserve |
| `huge` | 633.4 KiB | also crosses the headroom reserve (~158k est. tokens) |

### small (0.1 KiB) — nothing should engage

Baseline overhead **0.03ms**, and every mode stays within noise of it
(-0.02…+0.08ms). Caveman is the only pass with no size gate, so its +0.08ms is
the real cost of injecting the directive. The size gates work.

### large (96.9 KiB)

| mode | rps | ovh_avg | ovh_p95 | Δ ovh_avg | applied |
|---|---|---|---|---|---|
| baseline (off) | 336 | 0.09 | 1 | — | — |
| rtk light | 272 | 12.50 | 28 | +12.41 | rtk=light |
| rtk standard | 264 | 13.02 | 29 | +12.93 | rtk=standard |
| rtk aggressive | 294 | 11.76 | 26 | +11.70 | rtk=aggressive |
| rtk auto | 270 | 12.76 | 30 | +12.70 | rtk=auto |
| caveman lite | 262 | 15.18 | 35 | +15.12 | caveman=lite |
| caveman full | 286 | 14.19 | 31 | +14.13 | caveman=full |
| headroom conservative | 372 | 0.08 | 1 | -0.01 | — (under reserve) |
| headroom balanced | 356 | 0.09 | 1 | +0.00 | — (under reserve) |
| headroom aggressive | 372 | 0.08 | 1 | -0.01 | — (under reserve) |
| headroom auto | 363 | 0.09 | 1 | +0.00 | — (under reserve) |
| all three (std/full/balanced) | 231 | 20.66 | 46 | +20.57 | rtk=standard caveman=full |
| squoze (exclusive) | 287 | 12.00 | 28 | +11.91 | — (no blocks squeezed) |

### huge (633.4 KiB)

| mode | rps | ovh_avg | ovh_p95 | Δ ovh_avg | applied |
|---|---|---|---|---|---|
| baseline (off) | 97 | 0.11 | 1 | — | — |
| rtk light | 61 | 112.31 | 260 | +112.20 | rtk=light |
| rtk standard | 66 | 98.42 | 211 | +98.31 | rtk=standard |
| rtk aggressive | 64 | 109.81 | 211 | +109.70 | rtk=aggressive |
| rtk auto | 63 | 112.00 | 243 | +111.89 | rtk=auto |
| caveman lite | 60 | 134.02 | 279 | +133.91 | caveman=lite |
| caveman full | 63 | 125.09 | 241 | +124.98 | caveman=full |
| headroom conservative | 114 | 51.16 | 114 | +51.05 | headroom=conservative |
| headroom balanced | 123 | 48.02 | 111 | +47.91 | headroom=balanced |
| headroom aggressive | 128 | 43.21 | 97 | +43.10 | headroom=aggressive |
| headroom auto | 127 | 44.14 | 97 | +44.03 | headroom=auto |
| all three (std/full/balanced) | 118 | 48.18 | 109 | +48.07 | rtk=standard caveman=full |
| squoze (exclusive) | 31 | 438.56 | 637 | +438.45 | — (no blocks squeezed) |

### What the numbers say

- **The <5ms p95 target holds only for small bodies.** At 0.1 KiB the gateway
  adds 0.03ms. Optimizer cost scales with body size, not request count: RTK is
  ~12ms at 97 KiB and ~110ms at 633 KiB. That is CPU spent to save upstream
  tokens, so it is a deliberate trade, but it belongs in the docs — a p95 quoted
  from the `small` profile does not describe a tool-heavy agent loop.
- **Headroom is the only mode that pays for itself in wall-clock.** On `huge` it
  raises throughput above baseline (97 → 128 rps) because pruning shrinks the
  body the upstream has to read. Every other mode trades rps for tokens.
- **Optimizer cost dominates the gateway's own overhead by ~3 orders of
  magnitude** (0.09ms baseline vs 12-438ms). Tuning the proxy hot path further
  is pointless next to gating these passes correctly.
- **squoze is the most expensive pass by far** (438ms at 633 KiB, 31 rps) and
  reported `squoze=false` — it squeezed no blocks on this synthetic filler, so
  that is near-worst-case: full analysis cost, no benefit. It stays experimental
  and config-only for good reason. A representative-payload measurement is
  needed before it can be recommended.

### Fix found by this benchmark

Explicit headroom profiles on `large` originally cost **8.82-9.19ms while
pruning nothing**, whereas `headroom auto` reached the same no-op for 0.02ms.
`OptimizeRequest`'s fast path required `o.RTK` and no headroom, so any explicit
profile fell through to a full `json.Unmarshal` of the body before the prune's
own O(1) size guard concluded there was nothing to do. The fast path now skips
the parse whenever no enabled pass can fire (`internal/compression/optimize.go`),
which is byte-identical because a no-op pass already returned the original body.

| headroom on 96.9 KiB | before | after |
|---|---|---|
| conservative | 8.88ms / 303 rps | **0.08ms / 372 rps** |
| balanced | 9.19ms / 289 rps | **0.09ms / 356 rps** |
| aggressive | 8.82ms / 296 rps | **0.08ms / 372 rps** |

~110× less overhead and ~23% more throughput, with pruning on `huge` unchanged.
Covered by `TestOptimizeRequestFastPathMatchesNoOpPass` and siblings in
`internal/compression/optimize_test.go` (the single-pass pipeline had no direct
test coverage before).

## Notes

- Fake-upstream (`test/fakeupstream`) simulates OpenAI with 20ms artificial latency; real upstream (OpenAI/Anthropic) adds 200-800ms, so gateway overhead is negligible.
- For LiteLLM comparison, run `ghcr.io/berriai/litellm` with same fake-upstream as backend and same `wrk` load.
- See `ferro-labs/ai-gateway-performance-benchmarks` for independent methodology.
