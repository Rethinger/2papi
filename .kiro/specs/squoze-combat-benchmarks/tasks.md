# Tasks — Squoze Combat Benchmark Suite

Requirements: [requirements.md](requirements.md) · Design: [design.md](design.md)

## Phase 1 — Benchmark Payloads & Test Harness

- [x] **TSK-001**: Construct realistic datasets for Scenario 1 (Deadlock/Race Condition), Scenario 2 (Monorepo Build), and Scenario 3 (Multi-Turn Session).
  - Requirement: FR-1, FR-2, FR-3
  - Deliverables: `test/benchmarks/scenario1_deadlock.json`, `scenario2_monorepo.json`, `scenario3_multiturn.json`
  - Acceptance: Valid JSON payloads representing genuine agent tool calls and noise.

- [x] **TSK-002**: Implement the automated benchmark runner with ground truth assertions and telemetry extraction.
  - Requirement: NFR-1, NFR-2
  - Deliverables: `test/combat_suite.mjs`
  - Acceptance: Runs all 3 scenarios against both `claude-opus-5` (With Squoze) and `claude-opus-5-nosquoze` (Baseline), capturing tokens, latency, headers, and accuracy.

---

## Phase 2 — Execution & Analysis

- [x] **TSK-003**: Execute the combat benchmark suite live against `gorouter.app` via `2papi.exe`.
  - Requirement: AC-1.1, AC-1.2, AC-2.1, AC-3.1
  - Deliverables: `test/results/combat_suite_report.json`
  - Acceptance: 100% execution success across all scenarios; telemetry logged.

- [x] **TSK-004**: Generate the final comparative analysis and visualization report.
  - Requirement: NFR-2
  - Deliverables: Comprehensive benchmark summary with token savings, latency impact, accuracy scores, and gorouter cost impact.

---

## Phase 3 — SWE-bench Verified Industry Benchmark Integration

- [x] **TSK-005**: Extract and format authentic SWE-bench Verified instances (`pallets__flask-5014` and `django__django-16595`) with real tool outputs and test failure logs.
  - Requirement: NFR-2, FR-1
  - Deliverables: `test/benchmarks/swe_bench_flask_5014.json`, `swe_bench_django_16595.json`
  - Acceptance: Authentic problem statements, real repo files, and failing test logs.

- [x] **TSK-006**: Implement automated SWE-bench test harness and patch validator.
  - Requirement: NFR-1, NFR-2
  - Deliverables: `test/swe_bench_suite.mjs`
  - Acceptance: Executes live comparison on `gorouter.app` via `2papi`, measures token reduction, Squoze latency, and validates patch correctness.

---

## Phase 4 — 2026 Industry Benchmark Suite (TerminalBench v2.1 & Aider Polyglot)

- [x] **TSK-007**: Construct authentic benchmark instances for TerminalBench v2.1 (CLI process crash, Linux core dump, exit code 137 OOM) and Aider Polyglot (Rust/TS multi-file diff patch).
  - Requirement: NFR-2, FR-1
  - Deliverables: `test/benchmarks/terminalbench_oom_crash.json`, `test/benchmarks/aider_polyglot_rust_patch.json`
  - Acceptance: Realistic CLI logs, exit codes, and cross-file patch expectations.

- [x] **TSK-008**: Implement automated runner `test/terminalbench_aider_suite.mjs` and execute live comparison against `gorouter.app`.
  - Requirement: NFR-1, NFR-2
  - Deliverables: `test/results/terminalbench_aider_report.json`
  - Acceptance: 100% execution success, telemetry logged.

- [x] **TSK-009**: Update project README.md and documentation with official September 2026 State-of-the-Art benchmark scorecard and badges.
  - Requirement: NFR-2
  - Deliverables: `README.md`, `squoze/README.md`
  - Acceptance: Complete benchmark table, verified metrics, token savings, and Pass@1 rates.
