# Tasks — 2papi release automation

Requirements: [requirements.md](requirements.md)

## Phase 1 — Reachability (done)

- [x] **TSK-001**: Add a tag trigger so the release job is reachable.
  - Requirement: FR-1
  - Deliverables: `.github/workflows/ci.yml`
  - Detail: `on.push` gained `tags: ['v*']`. The job's
    `if: startsWith(github.ref, 'refs/tags/v')` was previously unreachable.
  - Acceptance: pushing a `v*` tag starts a run. ✅ committed in `97a75d2`
  - Evidence: run #2 on `master` shows Goreleaser correctly *skipped*
    (`event: push`, `headBranch: master`) — the gate works; it now needs a tag to
    fire.

## Phase 2 — Config hygiene

- [x] **TSK-002**: Remove deprecated goreleaser properties.
  - Requirement: FR-4, AC-4.1
  - Deliverables: `.goreleaser.yaml`
  - Detail: `archives.builds` → `ids`; `archives.format_overrides.format` →
    `formats` (list form). Do not touch `name_template` — `install.sh` depends on
    the current archive names (NFR-3).
  - Acceptance: `goreleaser check` exits 0 with no DEPRECATED lines.

- [x] **TSK-003**: Re-verify the build after the config edit.
  - Requirement: NFR-2, NFR-3
  - Deliverables: none (verification)
  - Detail: `goreleaser release --snapshot --clean --skip=publish`; diff the
    produced archive names against the pre-edit run
    (`2papi_linux_amd64.tar.gz`, `2papi_linux_arm64.tar.gz`,
    `2papi_darwin_amd64.tar.gz`, `2papi_darwin_arm64.tar.gz`,
    `2papi_windows_amd64.zip`).
  - Acceptance: identical archive set; names unchanged.

- [x] **TSK-004**: Fix the stale license in the commented tap blocks.
  - Requirement: Constraints
  - Deliverables: `.goreleaser.yaml`
  - Detail: commented brew/scoop blocks claim `license: MIT`; the repo is
    Apache-2.0. Correct the comment so a future tap does not inherit it.
  - Acceptance: no `MIT` string remains in the file.

- [x] **TSK-005**: Remove the phantom version fallback in `install.sh`.
  - Requirement: Edge cases
  - Deliverables: `install.sh`
  - Detail: line 35 falls back to `v0.1.0`, a tag that never existed. Fail with a
    clear message (pointing at `go install` / Docker) instead of chasing a
    nonexistent release.
  - Acceptance: with the API unreachable, the script explains the situation
    rather than 404-ing on a made-up tag.

## Phase 3 — First release

- [ ] **TSK-006**: Tag a release from current `master`.
  - Requirement: FR-1, AC-1.1, Edge cases
  - Detail: a **new** tag (`v0.3.0` — `master` is far ahead of `v0.2.0`: squoze
    dependency, optimization modes, guardrails, cache, OTel). Releasing
    `v0.2.0` would publish an August snapshot as "latest". Update
    `CHANGELOG.md` with a version heading first, then push the tag and let CI
    publish.
  - Acceptance: published Release exists with all five archives and
    `checksums.txt`.

- [ ] **TSK-007**: Verify the install path end to end.
  - Requirement: FR-3, AC-3.1, AC-3.2
  - Detail: in a container **without** Go, run `install.sh` and confirm it
    downloads a prebuilt binary (no "building from source" message) and that
    `2papi version` prints the tagged version rather than `dev`.
  - Acceptance: both hold.

- [ ] **TSK-008**: Update install docs to lead with the release path.
  - Requirement: FR-3
  - Deliverables: `README.md`
  - Detail: the README currently documents the no-release reality
    ("No published releases yet", `go install` first). Once a release exists,
    promote the script/binary path and delete that section.
  - Acceptance: no README statement about releases is false.

## Dependency graph

```
TSK-001 (done)
   ↓
TSK-002 → TSK-003
TSK-004 ─┤
TSK-005 ─┘
   ↓
TSK-006 → TSK-007 → TSK-008
```

## Progress

| Task | Status |
|---|---|
| TSK-001 tag trigger | Complete |
| TSK-002 drop deprecated properties | Complete |
| TSK-003 re-verify snapshot | Complete |
| TSK-004 stale MIT comment | Complete |
| TSK-005 install.sh phantom fallback | Complete |
| TSK-006 tag first release | Pending |
| TSK-007 verify install path | Pending |
| TSK-008 promote release path in README | Pending |
