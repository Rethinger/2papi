# Requirements — 2papi release automation

Status: in progress · Created 2026-09-02 · Quick Spec (design inline)

## Context / problem

2papi has **zero** GitHub Releases despite a pushed `v0.2.0` tag
(2026-08-20) and a fully working goreleaser config.

Root cause, established by inspection and testing:

1. `.github/workflows/ci.yml` declared `on: push: branches: [master]` with no
   `tags:` key, while the `release` job is gated on
   `if: startsWith(github.ref, 'refs/tags/v')`. A tag push started no workflow at
   all, so that condition could never be true — the job was **unreachable**.
   Fixed in `97a75d2` by adding `tags: ['v*']`.
2. No tag has been pushed since the fix, and `v0.2.0` will not retrigger.

The config itself is sound: `goreleaser release --snapshot` was run in a
container and built all five targets, producing
`2papi_{linux,darwin}_{amd64,arm64}.tar.gz` and `2papi_windows_amd64.zip` —
exactly the names `install.sh:50` constructs.

User-visible consequence: `install.sh` / `install.ps1` request
`releases/latest`, get a 404, fall back to building from source, and therefore
require a Go toolchain — contradicting the README's former "No Docker, no Go
required" claim (corrected in `97a75d2`).

## Functional requirements

- **FR-1 (Must)** — WHEN a tag matching `v*` is pushed, the system SHALL publish a
  GitHub Release with prebuilt archives and checksums.
  - **AC-1.1** — Given tag `vX.Y.Z` is pushed, When CI finishes, Then a published
    Release `vX.Y.Z` exists with the five archives and `checksums.txt`.
  - **AC-1.2** — Given the release job runs, Then it authenticates with the
    workflow `GITHUB_TOKEN` only.

- **FR-2 (Must)** — The release SHALL NOT publish unless the Go tests,
  control-plane tests and Docker build all pass (already expressed as
  `needs: [go-test, control-plane-test, docker]`).
  - **AC-2.1** — Given any required job fails on a tag push, Then no Release is
    created.

- **FR-3 (Must)** — WHEN a release exists, `install.sh` and `install.ps1` SHALL
  install a prebuilt binary without requiring Go.
  - **AC-3.1** — Given a published release, When `install.sh` runs on a machine
    without Go, Then it downloads and installs the binary and does not print
    "Release not found, building from source".
  - **AC-3.2** — Given the installed binary, When `2papi version` runs, Then it
    prints the released version — `main.version` is injected by goreleaser
    ldflags and defaults to `dev` otherwise (`cmd/gateway/main.go:35`).

- **FR-4 (Must)** — The goreleaser config SHALL be free of deprecated
  properties.
  - **AC-4.1** — Given the config, When `goreleaser check` runs, Then it exits 0
    with no DEPRECATED lines.
  - Currently **failing**: `archives.builds` and
    `archives.format_overrides.format` are deprecated. CI pins
    `goreleaser-action` `version: latest`, so a goreleaser v3 would break
    releases outright.

- **FR-5 (Should)** — Release notes SHALL come from commit history with `docs:`
  and `test:` commits excluded (already configured).

## Non-functional requirements

- **NFR-1** — Least privilege: `contents: write` stays scoped to the release job.
- **NFR-2** — Reproducible builds: `CGO_ENABLED=0` static binaries for
  linux/darwin/windows × amd64/arm64 (minus windows-arm64), as configured.
- **NFR-3** — Archive names must not change; `install.sh` derives them by
  convention and a rename silently breaks installs.
- **NFR-4** — `dist/` must remain gitignored (it is, `.gitignore:15`).

## Constraints

- Homebrew/scoop publishing stays commented out until `Rethinger/homebrew-tap`
  and `Rethinger/scoop-bucket` exist. Note the commented blocks say
  `license: MIT` while the repo is Apache-2.0 — fix the stale comment so it is
  not copied into a real tap later.
- Cutting a release is outward-facing and public; it happens only on explicit
  instruction.

## Edge cases

- `v0.2.0` predates the squoze dependency, the optimization modes, guardrails and
  cache. Releasing it as "latest" would ship an August snapshot to users, so the
  next release should be a **new** tag from current `master`, not a backfill of
  `v0.2.0`.
- `install.sh` falls back to `v0.1.0` when the API returns nothing
  (`install.sh:35`) — a tag that has never existed. Once releases exist the
  fallback is unreachable, but it is a latent trap worth removing.

## Related

- [../../../squoze/.kiro/specs/release-automation/requirements.md] — same class of
  defect; squoze additionally has no goreleaser config at all.
