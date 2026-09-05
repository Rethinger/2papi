# 2papi — Multi-account AI Gateway

[![CI](https://github.com/Rethinger/2papi/actions/workflows/ci.yml/badge.svg)](https://github.com/Rethinger/2papi/actions/workflows/ci.yml)
[![Squoze savings](https://img.shields.io/badge/Squoze%20savings-87--99.9%25%20on%20machine%20output-orange)](#squoze-compression-quality)
[![Gateway overhead](https://img.shields.io/badge/gateway%20overhead-0.02ms%20%40%2010%20conc-blue)](#gateway-overhead)
[![Wire conformance](https://img.shields.io/badge/OpenAI%20wire%20conformance-9%20pass%20%C2%B7%200%20fail-brightgreen)](#openai-wire-protocol-conformance)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

*Self-hosted, OpenAI-compatible gateway that pools Claude / ChatGPT / Gemini
subscription accounts behind one endpoint — with virtual keys, budgets, built-in
token savers and an MCP gateway. One static Go binary, no Python.*

**A Go AI gateway for teams that outgrew LiteLLM.** Same virtual-key/budget
model, but: **gateway overhead 0.02 ms at 10 concurrent** (measured below;
LiteLLM's Rust rewrite publishes a ~8 ms p95 target), one static binary with an
embedded dashboard (no Python ops tax), and three things no other Go gateway
has:

- **Token savers built in** — four request optimizers: RTK tool-result
  compression, Caveman terse mode, Headroom context pruning, and
  [Squoze](https://github.com/Rethinger/squoze) content-routed elision. Squoze
  is the one whose savings are measured here — 87–99.9% on machine output,
  [with the corpus and its failures](#squoze-compression-quality). The other
  three are described by mechanism below; this repo does not put a savings
  number on them.
- **Multi-account subscription pooling** — claude.ai cookies / Codex auth /
  OAuth tokens pooled behind public aliases with sticky sessions that preserve
  prompt cache hits.
- **MCP gateway behind budgets** — `POST /v1/mcp/<server>` JSON-RPC passthrough
  where your virtual-key budgets and RPM limits apply to tool calls.

Plus a semantic response cache (exact + Jaccard-similar, hit-rate in the
dashboard) and immutable config snapshots with rollback.

OpenAI Codex account setup, model discovery, quota/reset safety, and validation
are documented in [docs/codex-provider.md](docs/codex-provider.md).

## Three editions, one binary

| | OSS | Cloud | Enterprise |
|---|---|---|---|
| For | self-host devs & homelabs | hosted demo (PLG funnel) | companies: compliance, VPC |
| Gets | everything below, Apache-2.0 | OSS stack + signup/credits | license unlocks SSO/OIDC, organizations + org budgets, audit export |
| Gating | — | deployment | offline Ed25519 license file; features fail closed to OSS without it |

Strategy details: [docs/strategy-v3.md](docs/strategy-v3.md). Error codes: [docs/error-catalog.md](docs/error-catalog.md). Security policy: [SECURITY.md](SECURITY.md). Contributing: [CONTRIBUTING.md](CONTRIBUTING.md).

## Install

**Install script (no Go toolchain needed)** — downloads the prebuilt binary for
your platform from the [latest release](https://github.com/Rethinger/2papi/releases/latest):

```sh
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/Rethinger/2papi/master/install.sh | sh
# Windows (PowerShell)
irm https://raw.githubusercontent.com/Rethinger/2papi/master/install.ps1 | iex

2papi version   # 2papi 0.4.0 (commit …, built …)
```

If no prebuilt archive matches your platform the script builds from source, which
does require Go.

**Manual download:** grab an archive from
[Releases](https://github.com/Rethinger/2papi/releases/latest) — `2papi_<os>_<arch>.tar.gz`
(`.zip` on Windows), verify it against `checksums.txt`, and put the `2papi` binary
on your `PATH`.

**Docker (full stack — gateway + Postgres + Redis + control-plane):**

```sh
docker compose up --build
# or the gateway alone
docker build -t 2papi . && docker run -p 8080:8080 2papi
```

**Go toolchain** (for development, or platforms without an archive):

```sh
go install github.com/Rethinger/2papi/cmd/gateway@latest
gateway --config ~/.2papi/config.yaml
# Dashboard: http://localhost:8080/dashboard/   Gateway: http://localhost:8080/v1/chat/completions
```

> Two differences from the release binaries: `go install` names the binary
> `gateway` (after `cmd/gateway`) rather than `2papi`, and it reports
> `dev (commit none)` because version metadata is stamped at release time.
> Rename it to `2papi` to match the command names used throughout these docs.

*Brew/scoop taps publish once the companion `Rethinger/homebrew-tap` and `Rethinger/scoop-bucket` repos exist (see RELEASE.md).*

**Interactive controls (like 9router):**

```sh
2papi tui      # keyboard menu: Start / Providers / Quota / Plugins / 2papi.local
2papi init     # interactive: enable 2papi.local via mDNS (LAN-wide) or hosts (this machine)
2papi advert   # keep 2papi.local advertising over mDNS/Bonjour (useful on a LAN)
2papi --mdns --hostname 2papi.local   # gateway starts + advertises mDNS at once
```

`2papi.local` resolves two ways:
- **mDNS/Bonjour** (`2papi init` choice 1, or `--mdns`): pure-Go, no admin rights, works LAN-wide on macOS/Linux; Windows needs a multicast-capable NIC.
- **hosts entry** (`2papi init` choice 2): `127.0.0.1 2papi.local` in `/etc/hosts`, this machine only, requires sudo/admin.

**Zero-config free provider (no API key needed):** uncomment in `config/example.yaml`:
`adapter: opencode` + `credential: { kind: free }` — model alias `opencode-free` serves without any key.

**Quota:** `2papi --config ...` + providers report `X-Provider-Quota-*` → `GET /api/quota`
(combined % bar + per-provider breakdown for the dashboard).

Design system and widget console are in [`open-design/`](open-design/) — hand-drawn pencil style, iOS-like widgets. See `open-design/README.md`.

## Features

- `/healthz`, `/readyz`, `/v1/models`, `/v1/chat/completions`.
- Generic OpenAI-compatible upstream proxy with public model alias rewriting.
- Claude accounts: Anthropic API key, claude.ai OAuth token, or browser cookies (`sessionKey` from claude.ai) — dedicated "Add Claude account" entry in the dashboard.
- Token-saver optimizations like 9Router, toggled from the dashboard **or per-model/per-key**: RTK compression of large tool results, Caveman mode (terse replies), **Headroom** (auto-prune old tool history when context nears limit), and **Squoze** (content-routed elision of machine output). All also opt-in per request via `X-Gateway-Compress` / `X-Gateway-Caveman` / `X-Gateway-Headroom` (`X-Gateway-Headroom-Reserve` to tune). Mode presets and the single-pass pipeline are documented in [Optimization modes](#optimization-modes-token-savers).
- SSE and JSON response streaming without full response buffering.
- Multiple accounts per public model alias.
- Upstream proxies for every account and a global pool — all protocols (http/https/socks4/4a/5/5h) in any format (`http://user:pass@host:8080`, `socks5://host:1080`, `host:3128`, `host:3128:user:pass`, `[::1]:9090`, lists per line/comma/JSON). Round-robin rotation per request with failover; `X-Gateway-Proxy` response header shows the masked proxy used. The pool is managed in the dashboard (Settings → Proxy pool).
- Routing strategies: `priority`, `balanced`, `fastest`, `cheapest`, `quota-drain`, `fallback-chain`.
- **Multi-provider aliases (`sources[]`)**: one public model served by different providers with their own upstream model names, weights, and per-source pricing — the gateway rewrites per attempt and telemetry records the actual upstream.
- **Semantic response cache**: exact + Jaccard-similar matching with hit-rate/exact/similar stats in the dashboard.
- Virtual API keys with constant-time keyed-HMAC comparison, model allowlists, and RPM token buckets.
- Sticky affinity from `X-Gateway-Session`, `metadata.gateway_session`, or stable user/model fallback.
- Account cooldowns, circuit breakers, concurrency caps, and route diagnostic headers.
- **OpenTelemetry GenAI traces** (optional): set `OTEL_EXPORTER_OTLP_ENDPOINT` and each request emits a span with `gen_ai.*` attributes (model, tokens, status). No endpoint = zero OTel code on the hot path.
- Enterprise (license-gated): OIDC single sign-on for the dashboard, organizations above teams with org-budget caps, audit export (NDJSON). Cloud edition adds self-serve signup with email verification, a signup credit grant, and prepaid balance enforcement (`min(team budget, balance)`).

### MCP gateway

`POST /v1/mcp/<name>` exposes upstream Model Context Protocol servers behind
virtual-key auth: budgets, RPM and concurrency apply to tool calls, and every
call lands in request logs. Configure the servers in your config file —
control-plane CRUD for them is on the roadmap, file config is the OSS path:

```yaml
mcp_servers:
  - name: my-tools
    url: https://mcp.example.com/mcp
    headers: { Authorization: "Bearer <upstream-token>" }
```

```sh
curl http://localhost:8080/v1/mcp/my-tools \
  -H "Authorization: Bearer sk-cp-…" -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
```

## Optimization modes (token savers)

RTK, Caveman and Headroom run as a **single JSON pass** over every request body
(headroom → RTK → caveman), preserving provider prompt caches wherever possible
(RTK is idempotent by marker; tools/system rows are never touched). When
**Squoze** is enabled it is the *exclusive* request optimizer — the other three
are skipped for that request, and a config mixing squoze with them is rejected
at startup.

Toggles are global → per-model → per-virtual-key → per-request header, and
each level carries a **mode preset** (empty = legacy behavior):

| Optimizer | Mode field | Presets | What it does |
|---|---|---|---|
| RTK | `rtk_mode` | `light` · `standard` · `aggressive` · `auto` | head/tail elision of large tool and user results |
| Caveman | `caveman_mode` | `lite` · `full` · `auto` | injects a terse-output system directive |
| Headroom | `headroom_profile` | `conservative` · `balanced` · `aggressive` · `auto` | prunes old tool history as context nears the limit |
| Squoze | `squoze` | `true`/`false` (exclusive) | classifies each block and elides machine output only |

`auto` is resolved per request: RTK per block size (light/standard/aggressive),
Caveman by traffic shape (agentic → full, chat → lite), Headroom by estimated
context pressure (below half the reserve → no-op, cache-friendly epochs).

```yaml
optimization:
  rtk_compression: true
  rtk_mode: auto
  caveman: true
  caveman_mode: full
  headroom: true
  headroom_profile: aggressive
  headroom_reserve: 120000
  headroom_keep: 8
```

Per request, the same knobs work through headers, with names instead of
true/false: `X-Gateway-Compress: aggressive`, `X-Gateway-Caveman: lite`,
`X-Gateway-Headroom: auto`, plus `X-Gateway-Headroom-Reserve` to tune.
Responses echo what actually ran via `X-Gateway-RTK-Mode`,
`X-Gateway-Caveman-Mode`, `X-Gateway-Headroom-Profile`, `X-Gateway-Squoze`
and `X-Gateway-Saved-Bytes` / `X-Gateway-Saved-Tokens`.

**How much they save is measured only for Squoze.** The unit tests pin
*behaviour* for all four — that RTK fires above its size gate and is idempotent,
that Caveman reaches the system row without reordering user content, that
Headroom prunes oldest-first — but this repo has no harness that puts a savings
percentage on RTK, Caveman or Headroom. Treat any such figure you find in
9Router-era material as theirs, not as measured here. Squoze has the corpus
[below](#squoze-compression-quality); the honest way to size the other three on
your own traffic is `X-Gateway-Saved-Bytes` on your own requests.

**Cost of these passes.** They trade gateway CPU for upstream tokens, and the
cost scales with body size rather than request rate: measured against a
fake-upstream at 20 concurrent, RTK adds ~12 ms on a 97 KiB body and ~110 ms on
633 KiB, while the gateway's own overhead without optimizers stays at ~0.1 ms.
Headroom is the exception — on large bodies it raises throughput above baseline,
because pruning shrinks what the upstream has to read. Per-mode numbers, payload
profiles and methodology: [docs/benchmarks.md](docs/benchmarks.md).

**Reasoning models note**: reasoning-capable upstreams (DeepSeek R/V-series,
o-series, Claude extended thinking) spend your `max_tokens` on hidden
`reasoning_content` *before* any visible content — a small limit yields an
empty answer with `finish_reason:"length"`. Budget ≥512–2000 tokens for such
aliases, and prefer per-key/per-model Caveman to tame verbose thinking.

## Benchmarks

Every number below says what produced it and what it does *not* prove. Full
methodology, per-case data and the audit that removed the previous version of
this section: **[docs/benchmark-audit.md](docs/benchmark-audit.md)**. Status of
each stored report file: [test/results/README.md](test/results/README.md).

> **Retracted.** A previous revision claimed 100% Pass@1 on SWE-bench Verified,
> TerminalBench v2.1 and Aider Polyglot. Those claims did not survive an audit of
> the very files they cited: grading was substring matching rather than test
> execution, the task IDs did not exist in the upstream datasets, sample size was
> n=1 per condition, and Squoze was **inactive** (`squoze=false`,
> `savedBytes=0`) in 5 of the 8 runs whose savings were attributed to it — every
> fixture was under its 4 KB size gate. The fixtures are still in the repo, now
> labelled for what they are: [test/benchmarks/README.md](test/benchmarks/README.md).

### Squoze compression quality

Offline, deterministic, no API keys. 15 cases across 7 classes:

```sh
docker run --rm -v "$PWD:/src" -w /src golang:1.23 go run ./test/squozebench
node test/tokenscore.mjs   # after: npm install --no-save gpt-tokenizer
```

Two builds matter here, because they grade differently:

| squoze build | corpus | median savings when it fires | cross-turn prefix |
|---|---|---|---|
| **v0.2.0** — what `go.mod` pins today | 11 pass · **3 fail** · 1 known-limit | 96.14% | broken on both model families |
| working tree, **untagged** | **14 pass · 0 fail** · 1 known-limit | 97.02% | stable |

The three v0.2.0 failures are real contract violations, not fixture quibbles:
two Go source files were elided as if they were test output, the JSON envelope
(`has_more`, `next_cursor`, `total_count`) vanished when a list was lifted into a
table, and that lift was non-deterministic. All three are fixed upstream and
pinned by tests there; until a tag above v0.2.0 exists, a `go install` of this
gateway still gets the v0.2.0 behaviour. Reproduce either side:
[`test/squozebench/repro/`](test/squozebench/repro/).

Head-of-tree numbers, per case:

| Tool output | Byte savings | Token savings | Facts kept |
|---|---|---|---|
| go test, 3 000 lines, 2 FAILs buried mid-stream | 97.9% | 97.8% | 4/4 |
| pytest, 400 passing + trailing traceback | 87.4% | 86.9% | 3/3 |
| k8s logs, 1 200 lines, 2 critical lines | 96.1% | 96.1% | 2/2 |
| pnpm ANSI progress spam | 99.9% | 99.9% | 1/1 |
| unified diff + 300-line lockfile hunk | 99.0% | 98.9% | 1/1 |
| paginated JSON, 800 rows → Markdown table | 76.2% | 63.7% | 3/3 envelope fields |

Corpus total 318 138 → 41 805 tokens (86.9% saved), 9 of 15 cases touched.
Engine latency p50 0.06–4.8 ms, worst p95 6.5 ms in the committed run — one
repeat spiked to 14.9 ms on the same case under host contention, which is
measurement noise rather than a second code path.

**Byte savings are an honest proxy for token savings — with one documented
exception.** On elided machine output the two agree within 0.6 pp, so
`X-Gateway-Saved-Bytes` tracks what you are billed. Tabular lifting is the
exception: Markdown pipes tokenize worse than the JSON they replace, so 76.2% of
bytes is only 63.7% of tokens, a 12.5 pp overstatement. `tokenscore.mjs` flags
any case that drifts by ≥5 pp rather than leaving it to be discovered.

**Size gates work as designed.** A 3 KB log under the Claude preset's 4096-byte
gate passes through untouched, and the report records that as a pass, not as a
0% saving to be averaged in.

**One disclosed known limit.** A go test run with 200 distinct `FAIL` lines hits
`MaxKept=50`: 17 of 200 survive. That is a real recall loss, and the elision
marker says how many lines it dropped — a disclosed loss rather than a silent
one. It is graded `KNOWN-LIMIT`, never `PASS`.

### Gateway overhead

```sh
docker compose --profile bench up --build bench-runner        # concurrency tiers
docker compose --profile bench run --rm bench-matrix          # optimization-mode matrix
```

Fixed local fake upstream, no provider network in the loop. Reference numbers
from a Windows laptop running Docker Desktop (WSL2) — treat as a floor, Linux
bare-metal does better:

| concurrency | reqs | RPS | TTFB p50 | p95 | p99 | gateway overhead avg |
|---|---|---|---|---|---|---|
| 10 | 3 265 | 408 | 3 ms | 5 ms | 9 ms | **0.02 ms** |
| 50 | 14 376 | 1 797 | 6 ms | 13 ms | 22 ms | **0.10 ms** |
| 100 | 19 351 | 2 419 | 20 ms | 37 ms | 47 ms | **0.31 ms** |

Zero errors across 37k requests. The overhead column is the pure gateway cost
(total minus upstream wait) — the sub-millisecond claim refers to this number,
not to provider latency, and it holds for small bodies. Per-mode costs on large
bodies are in [docs/benchmarks.md](docs/benchmarks.md); note that its squoze row
is stale (documented 438 ms on 633 KiB, measured 74–147 ms after v0.2.0's fast
bailout). `BENCH_TIERS`, `BENCH_DURATION_MS` and `GATEWAY_URL` tune the runner.

### OpenAI wire-protocol conformance

```sh
node test/conformance.mjs      # needs a running stack; GATEWAY_KEY from env
```

`test/results/conformance_report.json`: **9 pass · 0 fail · 3 inconclusive.**
SSE framing terminates with `[DONE]`, frames stay well-formed through the proxy,
unknown keys get `401` and unknown models `404` with proper OpenAI error objects,
and optimizer echo headers report what actually ran. The 3 inconclusive checks
are limits of the bundled fake upstream (it always streams and never emits
`usage`), not gateway defects.

### Not measured

- **SWE-bench Verified / TerminalBench / Aider Polyglot.** Running these
  properly needs per-instance containers and real `FAIL_TO_PASS`/`PASS_TO_PASS`
  execution. Until that exists here, this repo makes no claim about them.
- **Whether compression changes model answers (Δaccuracy).** This is Level 2 of
  squoze's own [eval protocol](https://github.com/Rethinger/squoze/blob/main/docs/eval-protocol.md)
  (LoCoMo / RULER / BFCL v3, gates Δaccuracy ≤ 2 pp, savings ≥ 50%, overhead p95
  ≤ 15 ms). The harness is ready (`test/accuracy_suite.mjs`, k≥3 enforced) but
  the configured upstream served no working model on the last attempt, so it
  reports `BLOCKED` rather than a number.
- **RTK / Caveman / Headroom savings**, as covered under
  [Optimization modes](#optimization-modes-token-savers).

## Testing

Go unit tests with race detection:

```sh
docker run --rm -v "$PWD:/src" -w /src golang:1.22 go test -race ./...
docker run --rm -v "$PWD:/src" -w /src golang:1.22 go vet ./...
```

On Windows substitute `%cd%` (cmd) or `${PWD}` (PowerShell) for `$PWD`; from
Git Bash prefix the command with `MSYS_NO_PATHCONV=1` or the mount path is
mangled.

The host does not need Go installed — the official image is enough, and the same
image builds the container: `docker build -t 2papi-gateway .`

Control-plane integration tests (migrations, constraints, audit, envelope
encryption, compile/publish/rollback, gateway acknowledgements):

```sh
docker compose exec control-plane npm test
```

These require `TEST_DATABASE_URL`, which compose sets to a dedicated
`papi_control_test` database. Create it once with
`docker compose exec postgres createdb -U postgres papi_control_test`; without it
those tests skip instead of failing.

### The local stack

```sh
docker compose up --build
```

Dashboard at `http://localhost:13000`, gateway at `http://localhost:18080`, plus
PostgreSQL, Redis and fake OpenAI-compatible upstreams. Public status endpoint:
`GET /status` — build version, uptime and account/model counters, no secrets,
ready to feed an external status page.

```sh
curl http://localhost:18080/healthz
curl http://localhost:18080/v1/models
curl -N http://localhost:18080/v1/chat/completions \
  -H "Authorization: Bearer sk-gateway-dev" \
  -H "Content-Type: application/json" \
  -H "X-Gateway-Session: demo" \
  -d "{\"model\":\"gpt-dev\",\"stream\":true,\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"
```

Responses include `X-Gateway-Route` and `X-Gateway-Attempts`. Upstream
authorization is replaced and never forwarded from the client.

### End-to-end

```sh
node test/e2e.mjs      # against a running docker compose stack
```

Drives the whole lifecycle: create an account, attach it to a model alias,
publish, wait for the gateway to adopt that exact version, mint a virtual key,
publish again, issue an authenticated streaming request, assert an unknown key is
rejected with 401, roll back to the baseline version, and verify the restored
snapshot.

## Configuration

Start from `config/example.yaml`. It defines a versioned immutable snapshot:

- `virtual_keys`: client keys, allowed models, and RPM limits.
- `models`: public aliases mapped to upstream model IDs and account lists.
- `accounts`: OpenAI-compatible base URLs, API keys, and optional per-account `proxy` (any format, list allowed).
- `proxies` (optional): global upstream proxy pool for accounts without their own proxy.
- `routing`: strategy, sticky TTL, and max pre-commit attempts.
- `resilience`: cooldown and circuit-breaker thresholds.
- `mcp_servers` (optional): upstream MCP endpoints exposed at `/v1/mcp/<name>` behind virtual-key auth.

The request hot path uses only an immutable in-memory snapshot. The dashboard
stores desired state in PostgreSQL, publishes version notifications through
Redis, and the Go gateway atomically adopts validated snapshots while retaining
its last valid configuration if the control plane is unavailable.
