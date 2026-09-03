# Requirements — Uncontaminated SWE Benchmark Suite

## Overview
A contamination-free, multi-language software engineering benchmark suite designed to evaluate LLM coding agents (e.g. Claude Opus 5) through 2papi and Squoze v2. Unlike standard SWE-bench (which relies on over-trained legacy Python repositories like Django, SymPy, and Flask), this suite uses 10–15 modern, actively maintained, non-cliché repositories across Go, Rust, TypeScript, and Python with real production issues, realistic tool noise, and ground-truth verification.

---

## Functional Requirements

### FR-1: Multi-Ecosystem Repository Selection
- The suite SHALL encompass 10–15 mid-sized, actively maintained open-source repositories spanning 4 core ecosystems:
  - **Go**: `sourcegraph/conc`, `go-chi/chi`, `charmbracelet/lipgloss`, `allegro/bigcache`
  - **Rust**: `ratatui/ratatui`, `hyperium/http`, `crossbeam-rs/crossbeam`
  - **TypeScript/Node**: `honojs/hono`, `colinhacks/zod`, `sindresorhus/ky`, `pinojs/pino`
  - **Python**: `Textualize/rich`, `encode/httpx`, `samuelcolvin/watchfiles`, `tiangolo/sqlmodel`
- Priority: MUST
- Acceptance Criteria:
  - **AC-1.1**: Each selected repository must have a clearly documented issue, a reproducible failing test, and an official minimal PR patch.
  - **AC-1.2**: Repositories must avoid common benchmark contamination (no legacy Django/Sympy/Flask).

### FR-2: Realistic Agent Trajectory & Noise Injection Harness
- For each issue, the test instance SHALL simulate genuine developer/agent interactions:
  - Tool Call 1: `git_status` / `git_diff` including lockfile noise (`go.sum`, `Cargo.lock`, `pnpm-lock.yaml`).
  - Tool Call 2: `run_test` output containing ANSI progress bars, passing tests, and the failure trace.
  - Tool Call 3: File read of the target source module.
- Priority: MUST
- Acceptance Criteria:
  - **AC-2.1**: Test instances must faithfully model the token distribution of real coding agent sessions.

### FR-3: Dual-Mode Comparative Evaluation (Squoze v2 vs Baseline)
- The harness SHALL execute each task under two configurations:
  1. `With Squoze v2` (via model alias `claude-opus-5` on 2papi)
  2. `Without Squoze` (Baseline via `claude-opus-5-nosquoze`)
- Priority: MUST
- Acceptance Criteria:
  - **AC-3.1**: Measure input/output token counts, execution duration, and Squoze stream latency.
  - **AC-3.2**: Validate whether Squoze preserved necessary diagnostic context to resolve the issue.

### FR-4: Patch Precision & Diff Bloat Metric
- The harness SHALL assess patch quality beyond simple string matching:
  - Format validity: Syntactically correct Unified Diff (`diff --git`, `---`, `+++`, `@@`).
  - Correctness: Ground-truth fix presence without regressions.
  - Diff Bloat Ratio: Added/deleted lines compared to the minimal official PR patch.
- Priority: SHOULD
- Acceptance Criteria:
  - **AC-4.1**: Flag and penalize extraneous or destructive edits.

---

## Non-Functional Requirements

### NFR-1: Low Latency Proxy Overhead
- The gateway overhead for Squoze streaming distillation must remain $< 1.0$ ms per request.

### NFR-2: Automated Reporting & Telemetry Export
- Results must be programmatically saved in structured JSON (`test/results/uncontaminated_swe_report.json`) and summarized in human-readable Markdown.
