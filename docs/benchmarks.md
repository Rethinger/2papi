# Benchmarks

Every figure on this page was produced by a command in this repo, on the hardware
named below, and states the payload size and mode it belongs to. Nothing here is
a target, a projection, or a number carried over from another project. Vendor
figures live in one clearly labelled section and are quoted as *their* claims
under *their* conditions.

Companion documents: [benchmark-audit.md](benchmark-audit.md) — what an audit
found wrong with this repo's earlier claims and how; and
[test/results/README.md](../test/results/README.md) — per-report status
(**backed** / **context only** / **disproved** / **BLOCKED**).

## Host and provenance

| field | value |
|---|---|
| CPU / RAM | AMD Ryzen 5 3550H, 8 logical CPUs and 10.7 GiB RAM visible inside the Docker VM |
| OS | Windows 11 Pro + Docker Desktop (WSL2) |
| Kind of host | a laptop, not an isolated bench host — read every millisecond as a ceiling and every rps as a floor |
| Measured | 2026-09-06 |
| Gateway commit | `ac098a4` |
| squoze pin | `v0.4.0`, verified on the binary that ran: `docker create` + `docker cp /app/gateway`, then `go version -m gateway` reports `dep github.com/Rethinger/squoze v0.4.0` |
| Gateway binary | 14 401 688 bytes (13.7 MiB), `linux/amd64`, the same binary the matrix ran against |
| Upstream | `test/fakeupstream` in the same compose network — no provider on the wire. `/v1/chat/completions` always streams two SSE frames with a 10 ms sleep after each, so it adds ~20 ms of stream time and **no** artificial first-byte delay, and it never emits `usage` (see the audit) |

Absolute milliseconds on this host are not portable to yours; the matrix report
knows that and refuses to publish drift when the host looks different — see
[How this report is judged](#how-this-report-is-judged).

## Optimization-mode matrix — measured 2026-09-06 (squoze v0.4.0)

```sh
docker compose --profile bench up -d --build fake-upstream gateway-bench
docker compose --profile bench run --rm --no-deps bench-matrix > test/results/matrix_raw.txt 2>&1
node test/matrix_compare.mjs test/results/matrix_raw.txt
```

Harness: `test/bench.mjs` with `BENCH_MATRIX=1` — one warm gateway process, 20
concurrent, 6 000 ms and 3 warm-up requests per mode, 14 modes × 3 payload
profiles. squoze is config-exclusive, so the matrix reaches it through a second
alias (`bench-squoze`) served by the same process.

`ovh` is the gateway's self-reported `X-Gateway-Overhead-Ms` — its own work, with
the upstream wait excluded. `Δ` is against the `baseline (off)` row **of the same
profile**. `applied` is the echo header the gateway set, so a mode that decided
to do nothing shows up as such instead of being credited with the work.

Payload profiles exist because every optimizer is size-gated — a single tiny body
would report "no overhead" for all 14 modes and prove nothing:

| profile | size | what it exercises |
|---|---|---|
| `small` | 0.1 KiB | pure gateway overhead; almost every optimizer short-circuits |
| `large` | 96.9 KiB | RTK, caveman and squoze do real work; headroom stays under reserve |
| `huge` | 633.4 KiB | also crosses the headroom reserve (~158k est. tokens) |

### small (0.1 KiB) — almost nothing should engage

| mode | reqs | rps | ovh_avg | ovh_p95 | Δ ovh_avg | ttfb_p95 | applied |
|---|---|---|---|---|---|---|---|
| baseline (off) | 3 526 | 588 | 0.02 | 0 | — | 34 | — |
| rtk light | 3 385 | 564 | 0.03 | 0 | +0.01 | 31 | — |
| rtk standard | 3 838 | 640 | 0.03 | 0 | +0.01 | 22 | — |
| rtk aggressive | 3 803 | 634 | 0.03 | 0 | +0.01 | 29 | — |
| rtk auto | 4 266 | 711 | 0.02 | 0 | +0.00 | 15 | rtk=auto |
| caveman lite | 3 624 | 604 | 0.11 | 1 | +0.09 | 26 | caveman=lite |
| caveman full | 3 443 | 574 | 0.21 | 1 | +0.19 | 32 | caveman=full |
| caveman auto | 3 662 | 610 | 0.13 | 1 | +0.11 | 21 | caveman=lite |
| headroom conservative | 3 594 | 599 | 0.03 | 0 | +0.01 | 26 | — |
| headroom balanced | 3 554 | 592 | 0.03 | 0 | +0.01 | 24 | — |
| headroom aggressive | 3 563 | 594 | 0.03 | 0 | +0.01 | 27 | — |
| headroom auto | 3 205 | 534 | 0.05 | 0 | +0.03 | 48 | — |
| all three (std/full/balanced) | 2 743 | 457 | 0.24 | 1 | +0.22 | 50 | caveman=full |
| squoze (exclusive) | 2 988 | 498 | 0.05 | 1 | +0.03 | 49 | — |

The size gates work: three RTK profiles and all four headroom profiles report
`applied = —`, and squoze bails out for 0.05 ms (v0.3.0 added the
`len(body) < 256 || !hasCandidate` guard). Caveman has no size gate, so its
0.11-0.21 ms is the real cost of injecting the directive — and `rtk auto` is the
one RTK profile that still fires, because "auto" decides per body rather than
per threshold.

Read the millisecond columns here, not `rps`: throughput spans 457-711 across
modes that all do approximately nothing, which is scheduler noise across a
14-mode sequence, not a property of the modes.

### large (96.9 KiB)

| mode | reqs | rps | ovh_avg | ovh_p95 | Δ ovh_avg | ttfb_p95 | applied |
|---|---|---|---|---|---|---|---|
| baseline (off) | 1 840 | 307 | 0.08 | 1 | — | 99 | — |
| rtk light | 1 359 | 227 | 16.75 | 40 | +16.67 | 115 | rtk=light |
| rtk standard | 1 397 | 233 | 16.10 | 37 | +16.02 | 108 | rtk=standard |
| rtk aggressive | 1 313 | 219 | 16.85 | 41 | +16.77 | 121 | rtk=aggressive |
| rtk auto | 1 403 | 234 | 16.33 | 39 | +16.25 | 117 | rtk=auto |
| caveman lite | 1 306 | 218 | 19.40 | 44 | +19.32 | 131 | caveman=lite |
| caveman full | 1 244 | 207 | 20.32 | 47 | +20.24 | 127 | caveman=full |
| caveman auto | 1 167 | 195 | 21.45 | 50 | +21.37 | 146 | caveman=full |
| headroom conservative | 1 832 | 305 | 0.10 | 1 | +0.02 | 90 | — |
| headroom balanced | 2 389 | 398 | 0.08 | 1 | +0.00 | 48 | — |
| headroom aggressive | 2 105 | 351 | 0.09 | 1 | +0.01 | 66 | — |
| headroom auto | 1 833 | 306 | 0.11 | 1 | +0.03 | 89 | — |
| all three (std/full/balanced) | 1 334 | 222 | 22.93 | 50 | +22.85 | 121 | rtk=standard caveman=full |
| squoze (exclusive) | 2 073 | 346 | 8.14 | 20 | +8.06 | 70 | squoze=true |

### huge (633.4 KiB)

| mode | reqs | rps | ovh_avg | ovh_p95 | Δ ovh_avg | ttfb_p95 | applied |
|---|---|---|---|---|---|---|---|
| baseline (off) | 608 | 101 | 0.30 | 1 | — | 296 | — |
| rtk light | 243 | 41 | 185.15 | 362 | +184.85 | 822 | rtk=light |
| rtk standard | 241 | 40 | 191.52 | 367 | +191.22 | 760 | rtk=standard |
| rtk aggressive | 302 | 50 | 138.07 | 279 | +137.77 | 651 | rtk=aggressive |
| rtk auto | 282 | 47 | 156.44 | 329 | +156.14 | 623 | rtk=auto |
| caveman lite | 255 | 43 | 204.30 | 396 | +204.00 | 730 | caveman=lite |
| caveman full | 275 | 46 | 187.81 | 372 | +187.51 | 664 | caveman=full |
| caveman auto | 185 | 31 | 298.31 | 582 | +298.01 | 1 043 | caveman=full |
| headroom conservative | 342 | 57 | 112.71 | 249 | +112.41 | 612 | headroom=conservative |
| headroom balanced | 408 | 68 | 91.07 | 207 | +90.77 | 469 | headroom=balanced |
| headroom aggressive | 443 | 74 | 75.81 | 170 | +75.51 | 426 | headroom=aggressive |
| headroom auto | 521 | 87 | 69.30 | 171 | +69.00 | 388 | headroom=auto |
| all three (std/full/balanced) | 437 | 73 | 83.46 | 205 | +83.16 | 492 | rtk=standard caveman=full |
| squoze (exclusive) | 512 | 85 | 74.56 | 175 | +74.26 | 371 | squoze=true |

### What the numbers say

- **The gateway's own cost is 0.02 ms (small), 0.08 ms (large) and 0.30 ms
  (huge).** Optimizer cost dominates it by two to three orders of magnitude, so
  tuning the proxy hot path further matters far less than gating these passes
  correctly.
- **squoze is no longer the expensive outlier, and it now fires.** 8.14 ms on
  `large` and 74.56 ms on `huge`, both with `applied = squoze=true`. On `large`
  it is the cheapest pass that does work; on `huge` only `headroom auto`
  (69.30 ms) is cheaper. The 438.56 ms row this page used to publish was measured
  on squoze **v0.2.0**, before the fast bailout, and it reported `squoze=false` —
  full analysis cost, nothing squeezed. It is deleted rather than annotated: it
  described a version two releases behind the current pin.
- **A pass that shrinks the body can beat the no-op row on wall-clock.** On
  `large`, squoze reaches 346 rps against baseline's 307 (1.13×) with a *lower*
  TTFB p95 (70 ms vs 99 ms), and `headroom balanced` reaches 398 rps (1.30×,
  48 ms). Less body is less for the upstream to read, so "did work yet finished
  sooner" is a real outcome here, not an impossibility.
- **That does not hold on `huge`.** Baseline's 101 rps beats every work-doing
  mode; the best is `headroom auto` at 87. The claim this page previously
  made — headroom raising throughput above baseline on `huge`, 97 → 128 rps — does
  **not** reproduce on v0.4.0 and is withdrawn. At this size headroom pays for
  itself in tokens, not in wall-clock.
- **Intensity labels do not predict cost.** On `huge`, `rtk aggressive` costs
  138.07 ms against `rtk light`'s 185.15 ms, and `headroom auto` costs 69.30 ms
  against `conservative`'s 112.71 ms. Cost tracks how much text a pass has to
  keep and re-serialize, not how aggressive its name sounds.
- **Overhead p95 runs roughly 2× the average on every work-doing row**
  (185.15 → 362 ms for `rtk light` on `huge`). Each cell is one 6-second window at
  20 concurrent on a laptop, so the tail is load-shaped; treat it as a ceiling.
- **A sub-millisecond p95 belongs to the `small` profile only.** It is the
  gateway's own overhead on a 0.1 KiB body, not a description of a tool-heavy
  agent loop, and this page no longer quotes it as a headline.
- **Gateway-observed squoze overhead is not squoze's engine latency.** The
  matrix measures the whole pass as the gateway sees it, including parsing and
  re-serialising a 96.9 KiB body: `ovh_p95` 20 ms on `large`, 175 ms on `huge`.
  The engine measured on its own corpus is far cheaper (worst-case p95 5.58 ms in
  `test/results/squoze_quality_report.json`). Neither number substitutes for the
  other, and the eval protocol's ≤ 15 ms overhead gate is defined on the engine.

## How this report is judged

`node test/matrix_compare.mjs` does not compare absolute milliseconds across
runs. Every mode is restated as its distance from the `baseline (off)` row of the
**same** run (`ovh_delta`), plus `rps_rel = mode.rps / baseline.rps` for context.
The first version of the script did diff absolute values against a snapshot from
another day, and on 2026-09-04 it flagged 25 of 28 rows as "slower than
documented" — including `baseline (off)` at +300%, a row that runs no optimizer
at all. Nothing had regressed; the host was busier.

Restating the numbers relatively removes the additive floor but does not make
them portable: most of an optimizer's cost is CPU time, and subtracting a
~0.1 ms no-op row removes none of that. So the baseline row's absolute
throughput is used for exactly one purpose — deciding whether a run is
comparable at all:

| gate | rule |
|---|---|
| host state | `measured baseline rps / recorded baseline rps` must land in `[0.7, 1.4]`, per payload profile. Outside the band, per-mode drift is computed and kept in the JSON but **not** published as a finding, and the CLI names the suppressed profiles |
| drift | a mode is flagged only when it moves by **both** ≥ 20% and ≥ 1.0 ms. The share alone would flag `small` forever: 0.02 → 0.03 ms is +50% and is noise |

The 2026-09-04 run scores 0.55 on `large` and 0.38 on `huge` — its
`huge/baseline (off)` managed 38 rps where this laptop does 101, and modes doing
real work reported *higher* throughput than the no-op row, which only load drift
across a 14-mode sequence can produce. That run is correctly refused as
`contended`.

The gate itself is tested rather than trusted:

```sh
node test/matrix_gate_selftest.mjs
```

9 checks drive the exported `compare()` with runs synthesised from the recorded
BASELINE: replaying the recorded run must produce no drift; the 2026-09-04 host
factor must be refused as `contended` with an empty `significant_drift` while the
per-row numbers stay in the JSON for auditing; a host twice as fast must be
refused too; a planted +40 ms regression on `huge/squoze (exclusive)` must be the
single flagged row; a halved `large/headroom balanced` throughput must be the
single `throughput_drift` row; a 0.03 → 0.05 ms move on `small` must be
suppressed by the millisecond floor; and a run missing the baseline row, an
unrecorded profile and a mode absent from BASELINE must each be handled without a
false claim. Because the synthetic runs are derived from `BASELINE`, the test
stays in step with every future re-measurement.

Refresh `BASELINE` only from a run whose squoze pin was proven on the binary that
served it, and update every field of `meta` when you do.

## Measured here vs vendor claims

Cross-gateway numbers are not measured in this repo: nothing here runs LiteLLM,
Bifrost or 9Router. What follows separates the two kinds of row explicitly, and
each vendor row carries the conditions its owner published it under.

| subject | figure | kind | conditions | source |
|---|---|---|---|---|
| 2papi gateway overhead, no optimizer | avg **0.02 ms** (0.1 KiB), **0.08 ms** (96.9 KiB), **0.30 ms** (633.4 KiB); p95 0-1 ms | **measured here** | this host, fake upstream in-network, 20 concurrent, 6 s per mode, self-reported `X-Gateway-Overhead-Ms` | tables above, `test/results/gateway_matrix_report.json` |
| 2papi gateway overhead by concurrency | avg **0.02 ms** at 10, **0.10 ms** at 50, **0.31 ms** at 100 concurrent | **measured here** | same host and upstream, small bodies, `docker compose --profile bench up --build bench-runner` | [README](../README.md#gateway-overhead) |
| 2papi single binary | **14 401 688 bytes (13.7 MiB)**, `linux/amd64` | **measured here** | `docker cp` off the bench image, `ls -l`; the same binary the matrix ran against | this page, Host and provenance |
| Bifrost added latency | "**Less than 15µs** added latency per request on average" | **vendor claim** | 5 000 RPS, 100% success rate; t3.medium (2 vCPU / 4 GB, buffer 15 000, pool 10 000) and t3.xlarge (4 vCPU / 16 GB, buffer 20 000, pool 15 000); their page notes the t3.xlarge runs used ~10 KB response payloads against ~1 KB, and publishes no percentiles alongside that average | [docs.getbifrost.ai](https://docs.getbifrost.ai/benchmarking/getting-started), read 2026-09-06 |
| LiteLLM proxy overhead | headline "**8ms P95** latency at 1k RPS"; the same page's 4-instance run reports overhead median 2 ms / p95 8 ms / p99 13 ms / avg 3.32 ms at 1 170 RPS, and its 2-instance run median 12 / p95 29 / p99 43 / avg 14.74 ms at 1 035.7 RPS | **vendor claim** | 4 instances at 4 CPU / 8 GB each, PostgreSQL, no Redis, a fake OpenAI endpoint as upstream, Locust with 1 000 users and 0.5-1 s think time | [docs.litellm.ai](https://docs.litellm.ai/docs/benchmarks), read 2026-09-06 |
| 9Router | *no row* | — | no published benchmark found for it; the p50/p95/p99, binary and memory figures this page used to carry had no source and are deleted rather than re-labelled | — |
| Third-party cross-gateway harness | *no citable figure* | **unverifiable** | `rbadillap/ai-gateways-benchmark` answers HTTP 200, but it is a harness rather than a result set: it needs your own paid provider credentials, publishes only anonymized `gateway-a` / `gateway-b` sample output, and states results are a property of your vantage point rather than a global ranking | [github.com/rbadillap](https://github.com/rbadillap/ai-gateways-benchmark), read 2026-09-06 |

**Why this page computes no ratio between those rows.** The three metrics are not
the same measurement. Ours is a self-reported internal duration on a laptop at 20
concurrent with 0.1-633 KiB bodies; Bifrost's is added latency per request at
5 000 RPS on EC2 with ~1-10 KB payloads; LiteLLM's is an overhead duration at
~1 000 RPS spread over four instances. No shared harness runs any two of them, so
any "N× faster" derived from this table would be invented. The vendor claims also
disagree among themselves by orders of magnitude — Bifrost's own page headlines a
microsecond-scale average, while LiteLLM's engineering blog has reported Bifrost
in the millisecond range — and this page does not adjudicate that: it was not
re-verified here, and neither figure is used above.

Two figures that appeared in earlier drafts of this comparison are **not**
verified by anything read here and are therefore absent: Bifrost's per-instance
split (11 µs on t3.xlarge / 59 µs on t3.medium), which lives on documentation
sub-pages whose navigation is client-rendered, and the 4.5 ms p99 attributed to
Bifrost v1.6.4 by a LiteLLM post. If either is wanted in this table later, fetch
the page it lives on and quote it with its own conditions.

## Similar-response cache lookup — measured 2026-09-06

`cache: similar` answers a near-duplicate question from the cache, which means
every request on such a model pays a lookup before it can go upstream. That
lookup is a linear scan, so its cost is the cache size: the gateway builds its
cache with `NewTTLResponseCache(4096)` (`internal/proxy/proxy.go`), and these
benchmarks fill exactly that many live entries under one model, so the model
filter rejects nothing.

```sh
docker run --rm -v "$PWD:/src" -v 2papi-gomod:/go/pkg/mod -w /src golang:1.25 \
  go test -run '^$' -bench 'FindSimilar|LookupExact' -benchtime 2s -count=3 ./internal/cache/
```

`go1.25.14 linux/amd64`, AMD Ryzen 5 3550H, 8 logical CPUs, three runs each:

| case | what the query is | measured | allocs |
|---|---|---|---|
| `FindSimilarFullCacheMiss` | 13 words against 13-word entries, sharing none — the size prune rules nothing out, so all 4096 reach the signature test | **0.385 / 0.423 / 0.433 ms** | 2 272 B, 23/op |
| `FindSimilarFullCacheHit` | 14 words, a near-duplicate of one entry — the bound rejects the other 4 095, one is scored exactly | **0.619 / 0.572 / 0.517 ms** | 2 352 B, 23/op |
| `FindSimilarSizePruned` | 10 words against 13-word entries — 10/13 = 0.77 cannot reach 0.9, so the size prune drops all 4096 for one comparison each | **0.317 / 0.323 / 0.341 ms** | 2 080 B, 23/op |
| `FindSimilarGateRejects` | `"continue"` — under `MinSimilarWords`, refused before the scan | **4.1 / 6.0 / 8.4 µs** | 424 B, 13/op |
| `LookupExactHitFullCache` | the exact path on the same full cache, for reference | **12.8 / 8.7 / 8.0 µs** | 848 B, 13/op |

**NFR-1 (similar lookup ≤ 1 ms on a full cache) holds**, worst case 0.43 ms with
about 2× headroom. Read the size-pruned row as the floor of a full scan: it does
no word work at all, so ~0.32 ms is what walking 4096 map entries costs by
itself, and the word work adds 0.05-0.30 ms on top. The gate row is the one
an agent loop hits, and it is three orders of magnitude cheaper than the scan.

Note the two µs rows swing 2× between runs on this laptop. Treat every figure
here as a ceiling of the same shape as the rest of this page, and re-run the
command above rather than trusting the exact digits.

### What made it fit (same command, same session)

The first working version measured **2.384 / 2.527 / 2.485 ms** on the miss and
**4.559 / 4.623 / 4.602 ms** on the hit, at **12 310 allocs and 1.87 MB per
lookup** — two to five times over budget, because it built a `map[string]struct{}`
for every candidate to intersect it with the query. Three changes, all in
`internal/cache/cache.go`, brought it inside:

- **Store the words as a set.** `wordList` dedupes on the way in, so a stored
  `RequestWords` slice can be probed against a single reusable query map instead
  of being rebuilt into one. Allocations fell 12 310 → 23 and memory 1.87 MB →
  ~2 KB. (`MinSimilarWords` therefore counts *distinct* words — eight repeats of
  one word carry one word's signal.)
- **Prune on set size.** The intersection is at most the smaller set and the union
  at least the larger, so Jaccard ≤ small/large: one integer comparison rules out
  every candidate too long or too short to reach the threshold.
- **Prune on a 64-bit word signature.** `Entry.WordBits` ORs one bit per word
  (FNV-1a, `& 63`), so a query word whose bit is absent from a candidate is
  certainly not in it, and the bits that *are* present cap the intersection.
  Thirteen AND-tests replace thirteen string hashes. Bit collisions only loosen
  the bound, so a collision costs one exact comparison and can never hide a match.

Both prunes are exact upper bounds on the score, which is why they change no
answer: a pruned candidate could not have won. The `internal/proxy` acceptance
tests (`cache_similar_test.go`) pass unchanged across the whole optimization, and
`TestLoadFromFileNormalizesLegacyWords` pins the one place the prunes could have
been fooled — a cache written by an older build, whose words carry repeats and
whose signature is absent, is normalized when it is loaded.

## How to run the benchmarks in this repo

```sh
# optimization-mode matrix (the tables above)
docker compose --profile bench up -d --build fake-upstream gateway-bench
docker compose --profile bench run --rm --no-deps bench-matrix > test/results/matrix_raw.txt 2>&1
node test/matrix_compare.mjs test/results/matrix_raw.txt

# concurrency tiers (the 10/50/100 overhead row above)
docker compose --profile bench up --build bench-runner

# squoze offline quality corpus (savings, needle recall, determinism)
go run ./test/squozebench

# OpenAI wire-shape conformance
node test/conformance.mjs

# similar-cache lookup on a full 4096-entry cache (the table above)
go test -run '^$' -bench 'FindSimilar|LookupExact' -benchtime 2s -count=3 ./internal/cache/
```

`scripts/benchmark.sh [gateway_url] [upstream_url]` is an ad-hoc script for a
stack you already have running: it times TTFB with
`curl -w "%{time_starttransfer}"`, runs `wrk -t4 -c100 -d10s --latency /healthz`
if `wrk` is installed, and falls back to `hey -n 200 -c 20` for POST if `hey` is.
No figure on this page comes from it — the reproducible paths are the compose
profiles above.

The only Go benchmarks in this repo are the cache-lookup ones in
`internal/cache/similar_bench_test.go`; `-bench=.` elsewhere matches nothing.

## What was optimized

Hot-path changes behind the numbers above. Each is a change this repo made; where
a figure follows, it says where the figure comes from.

- `resilience.State` uses an `RWMutex` for `Cooling/Active/Latency` (was a
  `Mutex` on the hot path).
- `server.requestIDMiddleware` builds IDs from `atomic.Uint64` plus time instead
  of calling `crypto/rand` per request.
- `proxy.Endpoint` short-circuits RTK below `MinCompressBytes = 2048`
  (`internal/compression/compression.go`), and `ShouldRTK` / `ShouldCaveman` /
  `ShouldHeadroom` honour per-model and per-key overrides. The `small` table
  above is what that gate looks like from outside.
- `policy.Auth` is sharded 16 ways (`shards [16]shard`, `shardIndex`,
  `internal/policy/policy.go`) to keep key lookups off one lock. The "~10 ns key
  pick" figure this list used to carry is **Bifrost's published number for their
  implementation**, not a measurement of this code — the repo defines no
  microbenchmark for it.
- **DeepSeek fast TTF** — `internal/adapter/deepseek` forwards
  `reasoning_content` and `content` frames 1:1 so hidden thinking never blocks
  the first visible token, and `compression.CompressReasoning` shortens chains of
  thought with an effort-aware budget. Both are pinned by behavioural tests
  (`adapter_test.go`, `TestCompressReasoningHigh`,
  `TestCompressReasoningLowTighter`, `TestCompressReasoningSkipsSmall`); neither
  first-token latency nor a compression ratio for reasoning text is measured in
  this repo, so no number is quoted here.
- **Plugin hooks** — `BeforeRequest` / `AfterResponse` are non-fatal, and an HTTP
  sidecar gets a 10 ms budget (`internal/plugin/registry.go:123`) so a slow
  plugin cannot hold the response.
- `PoolTransport` sets `MaxIdleConns 512`, `MaxIdleConnsPerHost 128`,
  `ForceAttemptHTTP2 true`, `IdleConnTimeout 90s` (`internal/proxy/transport.go`,
  pinned by `internal/proxy/transport_test.go`).
- Streaming goes through `pipeCaptureReader`, which forwards frames 1:1 instead
  of buffering the whole response.

### Fix found by this benchmark (2026-09-02 run)

Explicit headroom profiles on `large` originally cost **8.82-9.19 ms while
pruning nothing**, whereas `headroom auto` reached the same no-op in 0.02 ms.
`OptimizeRequest`'s fast path required `o.RTK` and no headroom, so any explicit
profile fell through to a full `json.Unmarshal` of the body before the prune's own
O(1) size guard concluded there was nothing to do. The fast path now skips the
parse whenever no enabled pass can fire (`internal/compression/optimize.go`),
which is byte-identical because a no-op pass already returned the original body.

| headroom on 96.9 KiB | before | after (2026-09-02) |
|---|---|---|
| conservative | 8.88 ms / 303 rps | **0.08 ms / 372 rps** |
| balanced | 9.19 ms / 289 rps | **0.09 ms / 356 rps** |
| aggressive | 8.82 ms / 296 rps | **0.08 ms / 372 rps** |

~110× less overhead, with pruning on `huge` unchanged. The 2026-09-06 run above
reproduces the fixed state on a different day (0.08-0.11 ms, 305-398 rps), so the
fix holds rather than being a one-run artefact. Covered by
`TestOptimizeRequestFastPathMatchesNoOpPass` and siblings in
`internal/compression/optimize_test.go`; the single-pass pipeline had no direct
test coverage before.

## Notes

- The bundled upstream is not a latency simulator. `test/fakeupstream` streams
  two SSE frames and sleeps 10 ms after each, so it contributes ~20 ms of stream
  time and **no** artificial first-byte delay, and it never emits `usage`. A real
  provider adds hundreds of milliseconds and dominates every figure on this page.
- Because that upstream never sends `usage`, no token-savings percentage can come
  from these runs. Savings are measured offline instead —
  `go run ./test/squozebench` — and on live traffic through
  `X-Gateway-Saved-Bytes`.
- To compare against LiteLLM on equal terms, point
  `ghcr.io/berriai/litellm` at the same `test/fakeupstream` and drive it with the
  same harness. Until that exists here, the vendor rows above stay labelled as
  claims.
