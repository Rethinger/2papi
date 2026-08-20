# Release v0.2.0

**2papi — lightweight multi-account AI gateway.** Faster than LiteLLM (p95 ~3-5ms vs 15-40ms), single ~11MB binary, no Docker required to run.

## What's inside

- **8 provider families**: free/no-auth (`opencode`, `felo`, `qoder`), OAuth subscription (`cursor`, `copilot`, `kimi`), plus `deepseek`, `claude` (api/oauth/cookie), `codex`, `gemini` and any OpenAI-compatible upstream.
- **Token savers** (9Router-style): RTK (command-aware: git/test/grep), Caveman, Headroom — per-model/per-key, opt-in by `X-Gateway-*` headers.
- **DeepSeek-aware**: thinking-toggling, `reasoning_content` CoT compression (effort-aware), fast-TTF SSE (first content never blocked by CoT).
- **Quota dashboard**: `internal/quota` tracker + `GET /api/quota` and `GET /api/control/v1/quota` — combined % bar + per-provider breakdown (contract in `open-design/console/dashboard-plan.md`).
- **Subscription-CLI headers** (claude-code / codex) — quota burns like the official CLI.
- **2papi.local** over mDNS (Bonjour, pure Go) or `/etc/hosts`; `2papi tui/init/advert`.
- **Plugins** (ds-h-like, pragmatic): in-proc hooks + HTTP sidecars, 10ms TTF budget, `plugins:` in config, `GET /api/plugins`.
- **Lite mode**: single binary, embedded dashboard (`go:embed`), file-backed store — no Postgres/Redis.
- **Performance**: 16-way sharded policy Auth (~10ns key pick, Bifrost-style), RWMutex resilience, atomic request-ID, zero-copy streaming.

## Getting it

```sh
# Binary (after tags pushed)
curl -fsSL https://raw.githubusercontent.com/Rethinger/2papi/main/install.sh | sh
2papi tui    # interactive menu

# Docker
docker compose up --build
# or single binary
docker build -t 2papi . && docker run -p 8080:8080 2papi
```

## Pushing this release

From the repo root (run on a machine with GitHub access):

```sh
git push -u origin master
git tag v0.2.0
git push origin v0.2.0
# goreleaser fires on the tag (CI: .github/workflows/ci.yml) →
# Linux/macOS/Windows binaries + checksums.txt + install.sh
```

If GitHub Actions is not yet enabled, build manually:
```sh
goreleaser release --clean        # needs GITHUB_TOKEN
# or
make cross          # dist/2papi_{linux,darwin,windows}_{amd64,arm64}[.exe]
make build          # bin/2papi (host)
```
