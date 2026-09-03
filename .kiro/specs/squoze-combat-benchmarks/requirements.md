# Requirements — Squoze Combat Benchmark Suite

## Overview
A high-signal, production-grade combat benchmark suite designed to evaluate **Squoze v2** context distillation and **2papi** gateway routing against realistic engineering workflows with **Claude Opus 5** (`gorouter.app`).

Unlike synthetic stress-test prompts that trigger 10+ minute thinking loops, this suite tests real developer workflows where small details, hidden error traces, and cross-turn state matter, completing each run in 15–40 seconds.

---

## Requirements

### FR-1: High-Concurrency Incident & Silent Race Condition (Go / Distributed Systems)
- **WHEN** an agent is tasked with diagnosing a production failure, the system **SHALL** provide realistic context containing:
  1. A recent PR unified diff with subtle lock handling changes AND noisy lockfile diffs (`go.sum`).
  2. A load-test terminal log with 150+ lines of passing HTTP 200 logs, progress bars, and 3 subtle failure lines with microsecond timestamp race conditions.
  3. An OpenTelemetry trace JSON dump with homogeneous spans where only 1 span exhibits a mutex deadline timeout.
- **AC-1.1**: Squoze **SHALL** prune the lockfile diff, collapse passing terminal lines, and lift the JSON trace while preserving all error timestamps and stack traces (Never-Elide).
- **AC-1.2**: Squoze **SHALL** reduce input tokens by $\ge 50\%$.
- **AC-1.3**: The model output **SHALL** accurately identify the exact root cause (unreleased lock / race condition) and provide the correct code patch.

### FR-2: Monorepo Build Breakage & Module Collision (TypeScript / Turborepo)
- **WHEN** an agent investigates a broken monorepo build, the system **SHALL** supply:
  1. A 200-line build log containing compiler warnings, linter noise, and a specific circular dependency / CJS vs ESM `package.json` subpath export conflict.
  2. A `package-lock.json` diff of 4,000+ characters.
- **AC-2.1**: Squoze Diff Engine **SHALL** elide the lockfile diff to a summary line.
- **AC-2.2**: Squoze Terminal Engine **SHALL** fold repetitive compiler warnings while preserving the exact module resolution failure line.
- **AC-2.3**: The model **SHALL** correctly identify the broken export / circular import and generate the required config fix.

### FR-3: Multi-Turn Combat Session (Context Accumulation & Cross-Turn Dedup)
- **WHEN** an agent executes a 3-turn diagnostic workflow:
  - Turn 1: Initial test run (terminal output with 1 failed test).
  - Turn 2: Source inspection and config dump (file read + JSON config).
  - Turn 3: Patch verification and final test output.
- **AC-3.1**: The system **SHALL** measure cumulative context size and cost across all 3 turns.
- **AC-3.2**: Squoze Dedup Engine **SHALL** replace stale read blocks from Turn 1 and Turn 2 with reference markers in Turn 3.
- **AC-3.3**: Token growth **SHALL** remain linear/sublinear with Squoze vs quadratic without Squoze.

### NFR-1: Performance & Latency
- The gateway overhead for Squoze distillation **SHALL NOT** exceed 5 ms (target: $< 1$ ms via stream scanner).
- Each test execution **SHALL** complete within 15–45 seconds.

### NFR-2: Observability & Ground Truth Verification
- Telemetry headers (`X-Gateway-Squoze`, `X-Gateway-Squoze-Latency-Ms`, `X-Gateway-Saved-Bytes`) **SHALL** be recorded for every run.
- Automated assertion checks **SHALL** evaluate both diagnostic accuracy and code validity.
