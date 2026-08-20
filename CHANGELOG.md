# Changelog — 2papi

Format: decisions and notable additions, newest first. See docs/ for deep dives.

## 2026-08-20 — Provider adapters, quota, TUI, plugins, hostname, DeepSeek

### Speed (Bifrost-style)
- `internal/policy` — 16-way sharded `Auth` (was single `sync.Mutex`); ~10ns key pick at 5k RPS; team-budget moved to shared `teamMu` (fixes cross-key team rollup).
- `internal/resilience` — read paths on `RWMutex` (`Cooling/Latency/Active/...`).
- `internal/server` — request ID from `atomic.Uint64+time` (was `crypto/rand`); always regenerates, never trusts client `X-Request-ID`.
- `internal/compression` — command-aware RTK: `git diff` 10+10, `git log` 8+8, `cargo/npm/go test` keeps only failures, `grep` truncate.
- Plugin hooks (`BeforeRequest/AfterResponse`) non-fatal with 10ms sidecar budget → TTF preserved.

### DeepSeek optimization (own approach)
- `internal/adapter/deepseek` — thinking-aware OpenAI-compatible adapter; SSE streams `reasoning_content` then `content` 1:1 so thinking never blocks first content (fast TTF); model id rewritten per chunk; `discover_models` supported.
- `internal/compression` — `CompressReasoning` (CoT head/tail per `reasoning_effort` low/high), DeepSeek CJK token estimate.

### Providers (OmniRoute-style, no 9Router weight)
- `internal/adapter/thirdparty` — lightweight OpenAI-compatible adapters:
  - Free/no-auth: `opencode`, `felo`, `qoder` (credential kind `free`, 0 secrets).
  - OAuth subscription: `cursor`, `copilot`, `kimi` — Bearer + single-flight refresh via `oauthrefresh.Manager`, 401→refresh→retry.
- `internal/adapter/anthropic` — `claude-code` CLI headers (`anthropic-beta …claude-code-20250219`, `anthropic-billing-source: claude_code`).
- `internal/adapter/codex` — official `codex` CLI headers (`X-OpenAI-Client`, `X-Stainless-*`).
- `internal/config` — accepts `deepseek`, `opencode/free/felo/qoder` (kind free/api_key), `cursor/copilot/kimi` (oauth/free/api_key).

### Quota (dashboard contract)
- `internal/quota` — per-account tracker: `Observe` (X-Provider-Quota-*/codex credits), `ObserveRaw` (primary/secondary windows), `Summary` (Σused/Σlimit), `List` (sorted), family detection.
- `internal/proxy` — observes quota headers on every upstream response; tracker survives snapshot adoption.
- `GET /api/quota` (gateway) and `GET /api/control/v1/quota` (control-plane, from `request_events` + providers) — same shape for the Quota widget/bar.
- `internal/cache` — `SetWithRequest` stores RequestHash+Words, `FindSimilar` Jaccard semantic cache, `Stats` (hit_rate/exact/similar/misses), disk `SaveToFile/LoadFromFile`.

### Interactive (like 9router) + plugins (like dsh, pragmatic)
- `2papi tui` — keyboard menu (`Start/Dashboard/2papi.local/config/Quit`), raw `x/term`, line-menu fallback off-TTY; `2papi init` — interactive enable for `2papi.local`.
- `internal/hosts` — cross-platform hosts add/remove (Windows `%SystemRoot%`, `/etc/hosts`), idempotent, sudo hint.
- `internal/mdns` — pure-Go `2papi.local` over mDNS/Bonjour (zeroconf): `2papi advert` keeps it alive, `2papi --mdns` advertises at gateway start, `2papi init` asks mDNS (preferred) or hosts. Note: on Docker Desktop/Windows host-network multicast may be unavailable (`bad rdata` probe logs are harmless — falls back to hosts).
- `internal/plugin` — Registry with `BeforeRequest/AfterResponse/Compress` in-process hooks + HTTP sidecar (`endpoint/before|after`, 10ms timeout, non-fatal), config-declared `plugins:` in YAML; `GET /api/plugins`.

### Misc
- Single-binary Dockerfile (`CGO_ENABLED=0`, `-s -w`, distroless, ~11MB); Free/DeepSeek/OAuth presets in `config/example.yaml`; `install.sh/ps1` mention tui/init; `docs/benchmarks.md` DeepSeek fast-TTF + policy/cache rows; control-plane `lib/quota.ts` + test.

Earlier backlog (this session): grok-pool cut, RTK/Caveman/Headroom per-model/per-key, lite mode without Postgres, semantic cache, open-design dashboard plan (quota bar + `/quota` tab + widgets).
