# 2papi Multi-account AI Gateway

[![CI](https://github.com/Rethinger/2papi/actions/workflows/ci.yml/badge.svg)](https://github.com/Rethinger/2papi/actions/workflows/ci.yml)

**A Go AI gateway for teams that outgrew LiteLLM.** Same virtual-key/budget
model, but: **TTF overhead <5ms** (LiteLLM's Rust rewrite targets ~8ms p95),
one static binary with an embedded dashboard (no Python ops tax), and three
things no other Go gateway has:

- **Token savers built in** — RTK tool-result compression (−20–40% input), Caveman terse mode (up to −65% output), Headroom context pruning. Cheaper *per task*, not just per token.
- **Multi-account subscription pooling** — claude.ai cookies / Codex auth / OAuth tokens pooled behind public aliases with sticky sessions that preserve prompt cache hits.
- **MCP gateway behind budgets** — `POST /v1/mcp/<server>` JSON-RPC passthrough where your virtual-key budgets and RPM limits apply to tool calls.

Plus a semantic response cache (exact + Jaccard-similar, hit-rate in the dashboard) and immutable config snapshots with rollback.

OpenAI Codex account setup, model discovery, quota/reset safety, and validation are documented in [docs/codex-provider.md](docs/codex-provider.md).

## Three editions, one binary

| | OSS | Cloud | Enterprise |
|---|---|---|---|
| For | self-host devs & homelabs | hosted demo (PLG funnel) | companies: compliance, VPC |
| Gets | everything below, MIT-style Apache-2.0 | OSS stack + signup/credits | license unlocks SSO/OIDC, organizations + org budgets, audit export |
| Gating | — | deployment | offline Ed25519 license file; features fail closed to OSS without it |

Strategy details: [docs/strategy-v3.md](docs/strategy-v3.md). Error codes: [docs/error-catalog.md](docs/error-catalog.md). Security policy: [SECURITY.md](SECURITY.md). Contributing: [CONTRIBUTING.md](CONTRIBUTING.md).

## Quick Install (30s)

**No Docker, no Go required — single binary with embedded dashboard:**

```sh
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/Rethinger/2papi/master/install.sh | sh
2papi --config ~/.2papi/config.yaml
# Dashboard: http://localhost:8080/dashboard/   Gateway: http://localhost:8080/v1/chat/completions

# Windows (PowerShell)
irm https://raw.githubusercontent.com/Rethinger/2papi/master/install.ps1 | iex

# Interactive controls (like 9router):
2papi tui      # menu: Start / Providers / Quota / Plugins / 2papi.local
2papi init     # enable http://2papi.local via hosts (Y/n)

# Docker (full stack)
docker compose up --build
# or single binary container
docker build -t 2papi . && docker run -p 8080:8080 2papi
```

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
- Token-saver optimizations like 9Router, toggled from the dashboard **or per-model/per-key**: RTK compression of large tool results (saves 20-40% input tokens), Caveman mode (terse replies, saves up to 65% output tokens), and **Headroom** (auto-prune old tool history when context nears limit). All also opt-in per request via `X-Gateway-Compress` / `X-Gateway-Caveman` / `X-Gateway-Headroom` (`X-Gateway-Headroom-Reserve` to tune).
- **Reasoning models note**: reasoning-capable upstreams (DeepSeek R/V-series, o-series, Claude extended thinking) spend your `max_tokens` on hidden `reasoning_content` *before* any visible content — a small limit yields an empty answer with `finish_reason:"length"`. Budget ≥512–2000 tokens for such aliases, and prefer per-key/per-model Caveman to tame verbose thinking.
- SSE and JSON response streaming without full response buffering.
- **MCP gateway** (`POST /v1/mcp/<name>`): expose upstream Model Context Protocol servers behind virtual-key auth — budgets, RPM and concurrency apply to tool calls, every call lands in request logs. Configure in your config file:

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
- Multiple accounts per public model alias.
- Upstream proxies for every account and a global pool — all protocols (http/https/socks4/4a/5/5h) in any format (`http://user:pass@host:8080`, `socks5://host:1080`, `host:3128`, `host:3128:user:pass`, `[::1]:9090`, lists per line/comma/JSON). Round-robin rotation per request with failover; `X-Gateway-Proxy` response header shows the masked proxy used. The pool is managed in the dashboard (Settings → Proxy pool).
- Routing strategies: `priority`, `balanced`, `fastest`, `cheapest`, `quota-drain`, `fallback-chain`.
- **Multi-provider aliases (`sources[]`)**: one public model served by different providers with their own upstream model names, weights, and per-source pricing — the gateway rewrites per attempt and telemetry records the actual upstream.
- **MCP gateway**: configure `mcp_servers` in your config file and expose them at `/v1/mcp/<name>` behind virtual-key auth — budgets and RPM apply to tool calls; tool calls land in request logs. (Control-plane CRUD for MCP servers is on the roadmap; file config is the OSS path.)
- **Semantic response cache**: exact + Jaccard-similar matching with hit-rate/exact/similar stats in the dashboard.
- Virtual API keys with constant-time keyed-HMAC comparison, model allowlists, and RPM token buckets.
- Sticky affinity from `X-Gateway-Session`, `metadata.gateway_session`, or stable user/model fallback.
- Account cooldowns, circuit breakers, concurrency caps, and route diagnostic headers.
- Enterprise (license-gated): OIDC single sign-on for the dashboard, organizations above teams with org-budget caps, audit export (NDJSON). Cloud edition adds self-serve signup with email verification, a signup credit grant, and prepaid balance enforcement (`min(team budget, balance)`).

## Docker-first development

The host does not need Go installed. Use the official Go Docker image:

```sh
docker run --rm -v "%cd%:/src" -w /src golang:1.22 go test -race ./...
docker run --rm -v "%cd%:/src" -w /src golang:1.22 go vet ./...
docker build -t 2papi-gateway .
```

Run the complete local stack with dashboard, PostgreSQL, Redis, gateway, and fake OpenAI-compatible upstreams:

```sh
docker compose up --build
```

Open the dashboard at `http://localhost:13000`. The OpenAI-compatible gateway remains at `http://localhost:18080`.

Call the gateway:

```sh
curl http://localhost:18080/healthz
curl http://localhost:18080/v1/models
curl -N http://localhost:18080/v1/chat/completions \
  -H "Authorization: Bearer sk-gateway-dev" \
  -H "Content-Type: application/json" \
  -H "X-Gateway-Session: demo" \
  -d "{\"model\":\"gpt-dev\",\"stream\":true,\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"
```

Responses include `X-Gateway-Route` and `X-Gateway-Attempts`. Upstream authorization is replaced and never forwarded from the client.

## Testing

Go unit tests with race detection:

```sh
docker run --rm -v "%cd%:/src" -w /src golang:1.22 go test -race ./...
```

Control-plane integration tests (migrations, constraints, audit, envelope encryption, compile/publish/rollback, gateway acknowledgements):

```sh
docker compose exec control-plane npm test
```

The integration tests require `TEST_DATABASE_URL`, which compose sets to a dedicated `papi_control_test` database. Create it once with `docker compose exec postgres createdb -U postgres papi_control_test`; without it those tests skip instead of failing.

### Benchmark

Reproducible gateway-overhead benchmark (fixed local fake upstream, no provider network in the loop):

```sh
docker compose --profile bench up --build bench-runner
```

Prints RPS plus TTFB p50/p95/p99 and the gateway's self-reported overhead (`X-Gateway-Overhead-MS`) per concurrency tier. Tune with `BENCH_TIERS`, `BENCH_DURATION_MS`, `GATEWAY_URL`.

Full-stack E2E against a running `docker compose up` stack:

```sh
node test/e2e.mjs
```

The E2E script drives the whole lifecycle: create an account, attach it to a model alias, publish, wait for the gateway to adopt that exact version, mint a virtual key, publish again, issue an authenticated streaming request, assert an unknown key is rejected with 401, roll back to the baseline version, and verify the restored snapshot.

## Configuration

Start from `config/example.yaml`. It defines a versioned immutable snapshot:

- `virtual_keys`: client keys, allowed models, and RPM limits.
- `models`: public aliases mapped to upstream model IDs and account lists.
- `accounts`: OpenAI-compatible base URLs, API keys, and optional per-account `proxy` (any format, list allowed).
- `proxies` (optional): global upstream proxy pool for accounts without their own proxy.
- `routing`: strategy, sticky TTL, and max pre-commit attempts.
- `resilience`: cooldown and circuit-breaker thresholds.
- `mcp_servers` (optional): upstream MCP endpoints exposed at `/v1/mcp/<name>` behind virtual-key auth.

The request hot path uses only an immutable in-memory snapshot. The dashboard stores desired state in PostgreSQL, publishes version notifications through Redis, and the Go gateway atomically adopts validated snapshots while retaining its last valid configuration if the control plane is unavailable.
