# Contributing

Thanks for helping make 2papi better. Short version: **one spine step = one
commit**, tests green before you commit, and the enthusiast experience
(30-second install, zero required config, zero telemetry) is a product
feature — never break it.

## Repository layout

- `cmd/gateway`, `internal/` — the Go gateway (hot path on immutable
  snapshots; see `internal/config`, `internal/router`, `internal/policy`,
  `internal/proxy`).
- `control-plane/` — Next.js dashboard + operator API (PostgreSQL/Redis),
  migrations in `control-plane/migrations/`.
- `docs/strategy-v3.md` — product strategy (OSS + Enterprise primary, Cloud
  PLG); `docs/build-spine-specs.md` — specs for the build spine.
- `CHANGELOG.md` — decision journal, newest first.

## Local development

No local Go required — everything runs through containers:

```sh
# Go tests (race detector)
docker run --rm -v "%cd%:/src" -w /src golang:1.22 go test -race ./...

# Full stack (postgres, redis, control-plane, gateway, fake upstreams)
docker compose up --build

# Control-plane full test suite against a disposable database
docker compose run --rm --no-deps \
  -e DATABASE_URL=postgres://postgres:postgres@postgres:5432/papi_control_test \
  -e TEST_DATABASE_URL=postgres://postgres:postgres@postgres:5432/papi_control_test \
  -e ALLOW_PRIVATE_UPSTREAMS=false \
  control-plane npm test
```

CI (`.github/workflows/ci.yml`) runs exactly these: Go `vet` + `-race`,
the full control-plane suite with a Postgres service, and a Docker build.

## Ground rules

1. **Tests green, then commit.** Every behavior change ships with a test
   that fails without it.
2. **Conventional commits** (`feat(mcp): …`, `fix(policy): …`,
   `migrations: …`). Migrations are their own commit.
3. **CHANGELOG line in the same commit** as the change it describes.
4. **Enterprise features sleep without a license**: gate them through
   `internal/edition` / `control-plane/lib/edition.ts` and verify they fail
   closed in OSS mode (add a gate test).
5. **Don't break the enthusiast DX**: no new mandatory config, no telemetry,
   no startup dependencies beyond Postgres for the dashboard.
6. **Migrations are append-only** SQL files numbered sequentially; they must
   stay idempotent-safe under re-run (`IF NOT EXISTS`).
7. Windows contributors: run Go tests through Docker (`golang:1.22`), not a
   host toolchain.

## Security

See [SECURITY.md](SECURITY.md). Never file public issues for vulnerabilities.

## Proposals

Open an issue with the problem first (what broke, what's slow, what's
missing) before proposing a design. Keep RFCs small: one problem, one
measurable outcome.
