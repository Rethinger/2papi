# Tasks — Uncontaminated SWE Benchmark Suite

Requirements: [requirements.md](requirements.md) · Design: [design.md](design.md)

## Phase 1 — Curating Uncontaminated Tasks (Go, Rust, TypeScript, Python)

- [ ] **TSK-101**: Construct dataset for `sourcegraph/conc` (Go) — Issue #156 `ResultErrorPool.Wait` clipping with `go.sum` noise and `go test` output.
  - Requirement: FR-1, FR-2
  - Deliverables: `test/benchmarks/uncontam_conc_156.json`
  - Acceptance: Valid JSON, authentic failing test trace, realistic `go.sum` diff.

- [ ] **TSK-102**: Construct dataset for `ratatui/ratatui` (Rust) — Issue #942 Paragraph text wrapping panic with `Cargo.lock` diff and `cargo test` backtrace.
  - Requirement: FR-1, FR-2
  - Deliverables: `test/benchmarks/uncontam_ratatui_942.json`
  - Acceptance: Valid JSON, authentic rustc panic trace, ground-truth patch.

- [ ] **TSK-103**: Construct dataset for `honojs/hono` (TypeScript) — CSRF middleware multipart/form-data boundary rejection with `pnpm-lock.yaml` diff and vitest failure.
  - Requirement: FR-1, FR-2
  - Deliverables: `test/benchmarks/uncontam_hono_csrf.json`
  - Acceptance: Valid JSON, vitest test logs, ground-truth patch.

- [x] **TSK-104**: Construct dataset for `Textualize/rich` (Python) — Table column alignment with ANSI codes with pytest output and lockfile diff.
  - Requirement: FR-1, FR-2
  - Deliverables: `test/benchmarks/live_issue_rich_4208.json`
  - Acceptance: Valid JSON, pytest logs, ground-truth patch. (PASSED via Live Suite).

---

## Phase 2 — Benchmark Harness Implementation

- [ ] **TSK-105**: Implement automated runner `test/uncontaminated_swe_suite.mjs` with dual-mode evaluation (Squoze vs Baseline), BloatFactor calculation, and CRI scoring.
  - Requirement: FR-3, FR-4, NFR-1, NFR-2
  - Deliverables: `test/uncontaminated_swe_suite.mjs`
  - Acceptance: Runs all tasks sequentially, records TTFT, duration, tokens, patch correctness, and diff precision.

---

## Phase 3 — Live Execution & Reporting

- [ ] **TSK-106**: Execute the uncontaminated SWE benchmark suite live against `gorouter.app` (Claude Opus 5) via `2papi.exe`.
  - Requirement: AC-3.1, AC-3.2
  - Deliverables: `test/results/uncontaminated_swe_report.json`
  - Acceptance: Execution success across all tasks; comparative metrics logged.

- [ ] **TSK-107**: Generate final analysis artifact and update project documentation.
  - Requirement: NFR-2
  - Deliverables: Comprehensive Markdown report.
