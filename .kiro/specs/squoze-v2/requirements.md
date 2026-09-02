# Requirements — Squoze v2: Attention-Sharpening Context Distiller

Status: Implemented & Verified · Created: 2026-09-02 · Methodology: Kiro Spec (Feature Spec)

## 1. Context and Vision

### 1.1 The Strategic Positioning
- **2papi Cloud (Primary Revenue):** Squoze is the secret weapon for margin expansion. In agentic workflows (OpenCode, Claude Code, Cursor, Aider), raw tool outputs represent 70–85% of ingested input tokens. By distilling tool noise while improving LLM reasoning quality, 2papi Cloud delivers higher task success rates at a fraction of upstream token costs.
- **2papi Self-Hosted (Stealth 9Router Replacement):** A zero-dependency, single static Go binary providing seamless multi-account subscription pooling (Claude cookies, Codex auth, OAuth), local mDNS, and Squoze v2 as an exclusive, next-generation context optimizer that outperforms legacy regex truncation.
- **2papi Enterprise (Organic Growth):** Hard compliance, audit logs, zero data leakage, and deterministic offline execution.

### 1.2 The Problem with Existing Optimizers (and Squoze v1)
1. **Naive Truncation (RTK / 9Router / Squoze v1):** Squoze v1 was limited to middle-line elision of test logs. On general agent payloads (large JSON outputs, git diffs, file reads), it squeezed nothing (`blocksSqueezed=0`, `squoze=false`), yet suffered massive latency overhead (**438ms on a 633 KiB payload** due to repeated $O(N^2)$ `gjson.GetBytes` / `sjson.SetBytes` scans over message arrays).
2. **Heavy Model-Based Compression (LLMLingua):** Requires local transformer models (PyTorch/ONNX), consuming 500MB+ RAM and adding 150–600ms latency, completely breaking the sub-5ms Go gateway SLA.
3. **Context Distraction & Attention Dilution:** Research shows LLM reasoning degrades as context length grows ("Lost in the Middle" and attention weight dilution). Terminal ANSI escape sequences, duplicate file reads across turns, raw JSON envelope bloat (nulls, metadata, trace IDs), and 4,000-line lockfile diffs actively dilute attention heads away from critical code and instructions.

### 1.3 The Squoze v2 Breakthrough
Squoze v2 transforms from a simple log trimmer into an **Attention-Sharpening Context Distiller**:
- **Faster:** Single-pass streaming byte parser ($O(N)$) achieving **<2.5ms p95 latency on 633 KiB bodies** (a >170× speedup).
- **Better Quality (Attention Sharpening):** Removes toxic token noise (ANSI escapes, lockfile diffs, JSON nulls/boilerplate, duplicate historical file reads) so the LLM attends directly to high-entropy signal.
- **Zero Hallucination Risk:** Never summarizes or rewrites code. Uses deterministic structural filtering and semantic reference markers.
- **100% Cache-Safe:** Byte-stable hash memo preserves provider prompt cache hits across turns.
- **Reversible:** All elisions embed a 12-char SHA-256 ref (`ref ab12cd34ef56`) retrievable locally.

---

## 2. User Stories

1. **US-1 (Agent Developer):** As a developer running coding agents, I want my agent to receive clean, distilled tool responses without lockfile diffs, ANSI escape sequences, or JSON envelope bloat, so that the model reasons more accurately, makes fewer mistakes, and finishes tasks faster.
2. **US-2 (2papi Cloud Operator):** As the 2papi Cloud operator, I want incoming agent requests to consume 30–60% fewer upstream input tokens while delivering faster TTFT (Time to First Token) to users, so that platform gross margins increase without degrading user experience.
3. **US-3 (Self-Hosted Developer):** As a self-hosted user switching from 9Router, I want a single Go binary that optimizes context out of the box with near-zero latency (<3ms) without requiring Python, Docker, or GPU runtimes.

---

## 3. Functional Requirements (EARS Format)

### FR-1: Single-Pass Zero-Allocation Architecture (Speed & Latency)
- **FR-1.1:** WHEN a request body is submitted to Squoze, the system SHALL locate and parse candidate tool messages in a single streaming pass ($O(N)$) without repeated full-buffer scans.
  - **AC-1.1.1:** Given a 633 KiB request body with 40 messages, processing time SHALL be less than 5.0ms on standard x86_64 hardware.
  - **AC-1.1.2:** Memory allocations during parsing SHALL NOT exceed 2× the size of the request body.
- **FR-1.2:** IF a request contains no tool messages or the total body size is below `MinRequestBytes` (default: 2,048 bytes), THEN Squoze SHALL bail out within 0.05ms and return the original slice untouched.

### FR-2: Terminal and ANSI Noise Sanitization (Signal Purification)
- **FR-2.1:** WHEN a tool message contains ANSI escape sequences (colors, cursor repositioning, progress bar redraws) or carriage-return overwrites (`\r`), the system SHALL sanitize them into clean text.
  - **AC-2.1.1:** Given command output with spinner frames or terminal colors, all escape bytes (`\x1b[...]`) SHALL be stripped.
- **FR-2.2:** WHEN a tool message contains consecutive identical lines (e.g. repeated build polling or log spam >3 identical lines), the system SHALL fold them into a single line with count annotation: `[... repeated X times ...]`.

### FR-3: Diff & Patch Distillation (Diff-Squoze)
- **FR-3.1:** WHEN a tool message contains a unified diff, the system SHALL identify generated lockfiles and binary assets (`package-lock.json`, `pnpm-lock.yaml`, `yarn.lock`, `go.sum`, `Cargo.lock`, `poetry.lock`, `*.min.js`, `*.map`).
  - **AC-3.1.1:** Lockfile diff sections exceeding 50 lines SHALL be replaced with a single structural marker: `[... squoze: %d lines of %s diff elided · ref %s ...]`.
  - **AC-3.1.2:** Actual source code diffs (e.g., `*.go`, `*.ts`, `*.py`) SHALL NOT be elided unless unchanged context lines exceed 8 lines, in which case context SHALL be compacted to 3 lines.

### FR-4: JSON Structural Pruning (J-Squoze)
- **FR-4.1:** WHEN a tool message contains a valid JSON payload exceeding 1,024 bytes, the system SHALL execute structural pruning.
  - **AC-4.1.1:** All top-level and nested keys with `null` values SHALL be removed.
  - **AC-4.1.2:** Empty collections (`[]` and `{}`) SHALL be pruned unless they are the root.
  - **AC-4.1.3:** Recognized metadata noise keys (`__typename`, `_links`, `trace_id`, `request_id`, `etag`) SHALL be stripped.
  - **AC-4.1.4:** Pruning SHALL reduce JSON token count by at least 15% without altering any non-null business data fields.

### FR-5: Cross-Turn Stale Read Deduplication (Dedup-Squoze)
- **FR-5.1:** WHEN an earlier turn ($T_{prev}$) contains a file view or read result that is repeated verbatim in a later turn ($T_{curr}$), the system SHALL preserve the latest instance ($T_{curr}$) and compact the earlier instance ($T_{prev}$).
  - **AC-5.1.1:** The older instance SHALL be replaced with: `[... squoze: earlier view of %s identical to turn %d · ref %s ...]`.
  - **AC-5.1.2:** The active working context in $T_{curr}$ SHALL remain 100% intact.

### FR-6: Error and Failure Immunity (Never-Elide Contract)
- **FR-6.1:** WHEN any content block contains error indicators (`FAIL`, `ERROR`, `panic:`, `Traceback`, `Exception`, non-zero exit code), the system SHALL NEVER elide the error line or its immediate context (3 lines before, 5 lines after).
  - **AC-6.1.1:** Even under aggressive compression, failure diagnostics SHALL survive verbatim.

### FR-7: Cache Stability and Reversibility
- **FR-7.1:** WHEN Squoze processes a content block, the system SHALL store a deterministic hash in an in-memory memo cache.
  - **AC-7.1.1:** Given identical input text across subsequent turns, Squoze SHALL emit byte-identical output to preserve upstream prompt caching.
- **FR-7.2:** WHEN any elision occurs, the system SHALL store the original text in the local originals store indexed by its 12-character SHA-256 reference.

---

## 4. Non-Functional Requirements (NFRs)

- **NFR-1 (Latency Overhead):** Squoze p95 latency overhead SHALL be <1.0ms for requests <100 KiB, and <3.0ms for requests up to 1 MiB.
- **NFR-2 (Zero Dependencies):** Squoze SHALL be written in pure Go standard library (with minimal lightweight utility packages, no CGO, no external Python or ONNX dependencies).
- **NFR-3 (Quality Uplift):** Squoze SHALL improve or maintain LLM task completion accuracy on benchmark datasets (LoCoMo / BFCL) by reducing attention noise, with $\Delta \text{accuracy} \ge 0.0\%$.
- **NFR-4 (Fail-Open Safety):** Any internal panic, malformed JSON, or unexpected format SHALL immediately fail-open, returning the original unmodified body without dropping client requests.

---

## 5. Constraints & Boundaries

- Squoze v2 remains an **exclusive mode** in 2papi: when `squoze: true` is configured, RTK, Caveman, and Headroom are bypassed because Squoze performs holistic, structure-aware distillation.
- Squoze NEVER modifies user instructions (`role: user` initial prompt) or system directives (`role: system`). Only tool outputs (`role: tool` in OpenAI, `type: tool_result` in Anthropic) and stale historical tool reads are subject to distillation.
