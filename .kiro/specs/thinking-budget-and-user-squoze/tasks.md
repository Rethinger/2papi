# Tasks — Thinking Budget & User Tool Squoze

Requirements: [requirements.md](requirements.md) · Design: [design.md](design.md)

## Phase 1 — Thinking Budget in 2papi

- [x] **TSK-201**: Extend `config.Model` with `ThinkingBudget` and implement `protocol.RewriteModelAndThinking`.
  - Requirement: FR-1
  - Deliverables: `internal/config/config.go`, `internal/protocol/protocol.go`
  - Acceptance: Correct JSON serialization, thinking block injection.

- [x] **TSK-202**: Wire `ThinkingBudget` in `adapter/openai/openai.go` and `proxy.go` with `X-Gateway-Thinking-Budget` header support.
  - Requirement: FR-1, AC-1.1, AC-1.2
  - Deliverables: `internal/adapter/openai/openai.go`, `internal/proxy/proxy.go`
  - Acceptance: Header and config overrides correctly rewrite upstream requests.

- [x] **TSK-203**: Add unit tests for `protocol.RewriteModelAndThinking`.
  - Requirement: AC-1.1
  - Deliverables: `internal/protocol/protocol_test.go`
  - Acceptance: All tests pass.

---

## Phase 2 — User Tool Squoze Scanner in Squoze Engine

- [x] **TSK-204**: Implement `user_tool_scanner.go` in `squoze/internal/engine/` supporting XML tags, code fences, and lockfile diffs.
  - Requirement: FR-2, AC-2.1, AC-2.2
  - Deliverables: `C:/Users/rethi/Documents/Projects/squoze/internal/engine/user_tool_scanner.go`
  - Acceptance: Human text preserved 100% verbatim; machine blocks distilled.

- [x] **TSK-205**: Integrate `distillUserContent` into `stream_scanner.go` for OpenAI messages loop.
  - Requirement: FR-2, AC-2.3
  - Deliverables: `C:/Users/rethi/Documents/Projects/squoze/internal/engine/stream_scanner.go`
  - Acceptance: Squoze scans and compresses `role: "user"` tool outputs seamlessly.

- [x] **TSK-206**: Add comprehensive unit tests for user tool squoze.
  - Requirement: AC-2.1, AC-2.2
  - Deliverables: `C:/Users/rethi/Documents/Projects/squoze/internal/engine/user_tool_scanner_test.go`
  - Acceptance: Tests verify XML `<tool_output>`, fenced ````terminal`, and pure human prompt protection.

---

## Phase 3 — Live Verification & Benchmark

- [x] **TSK-207**: Run a live test verifying both `thinking_budget` bounding and `role: "user"` compression against `gorouter.app`.
  - Requirement: NFR-1, NFR-2
  - Deliverables: `test/test_thinking_and_user_squoze.mjs`
  - Acceptance: Verified speedup and byte reduction.

---

## Progress Tracking

| Task ID | Description | Status | Evidence |
|---|---|---|---|
| **TSK-201** | Model `thinking_budget` & Protocol rewriter | ✅ Complete | `internal/protocol/protocol.go:RewriteModelAndThinking` |
| **TSK-202** | OpenAI adapter & Header override wiring | ✅ Complete | `internal/adapter/openai/openai.go` |
| **TSK-203** | Protocol thinking unit tests | ✅ Complete | `TestRewriteModelAndThinking` passed |
| **TSK-204** | User tool squoze scanner implementation | ✅ Complete | `squoze/internal/engine/user_tool_scanner.go` |
| **TSK-205** | Stream scanner integration | ✅ Complete | `squoze/internal/engine/stream_scanner.go` |
| **TSK-206** | User squoze unit tests | ✅ Complete | `TestDistillUserContentXML`, `PureHuman`, `LockfileDiff` passed |
| **TSK-207** | Live end-to-end verification | ✅ Complete | `test/test_thinking_and_user_squoze.mjs` (11.3KB pruned, 33s vs 165s) |
