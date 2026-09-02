# Tasks — Squoze v2: Attention-Sharpening Context Distiller

Requirements: [requirements.md](requirements.md) · Design: [design.md](design.md)

## Phase 1 — Core Engine & Streaming Parser

- [x] **TSK-001**: Implement fast bailout check and single-pass streaming scanner.
  - Requirement: FR-1, AC-1.1.1, AC-1.1.2, FR-1.2
  - Deliverables: `squoze/internal/engine/stream_scanner.go`, `squoze/internal/engine/engine.go`
  - Detail: Replaced $O(N^2)$ `gjson.GetBytes` loop with single-pass sequential scanner. Locates tool messages and stitches replacements with zero-copy buffer.
  - Acceptance: Multi-message 40-turn payload runs in ~2.87ms (down from 438ms).

- [x] **TSK-002**: Benchmark and unit-test streaming parser against OpenAI and Anthropic wire formats.
  - Requirement: FR-1, NFR-4
  - Deliverables: `squoze/internal/engine/stream_scanner_test.go`
  - Detail: Validated boundary identification and slice reconstruction on edge cases: empty messages, multi-block tool results, malformed JSON (fail-open).
  - Acceptance: 100% test pass; fail-open verified on malformed inputs.

---

## Phase 2 — Distillation Modules (Signal Purification)

- [x] **TSK-003**: Implement ANSI & Terminal Noise Sanitizer with line folding.
  - Requirement: FR-2, AC-2.1.1, AC-2.2
  - Deliverables: `squoze/internal/distill/terminal.go`, `terminal_test.go`
  - Detail: Strip ANSI CSI escape sequences, resolve carriage-return progress overwrites (`\r`), and fold consecutive identical lines (>3 identical lines).
  - Acceptance: Clean text output without escape codes; repetitive lines folded with accurate counts.

- [x] **TSK-004**: Implement Diff & Lockfile Distiller (Diff-Squoze).
  - Requirement: FR-3, AC-3.1.1, AC-3.1.2
  - Deliverables: `squoze/internal/distill/diff.go`, `diff_test.go`
  - Detail: Detect unified diff headers. Elide generated lockfile diffs (`package-lock.json`, `go.sum`, `Cargo.lock`, etc.) with semantic summary markers. Compact unchanged context lines >8 lines to 3 lines.
  - Acceptance: Lockfile diffs elided by >95%; application source code diffs preserved verbatim.

- [x] **TSK-005**: Implement JSON Structural Pruner (J-Squoze).
  - Requirement: FR-4, AC-4.1.1 - AC-4.1.4
  - Deliverables: `squoze/internal/distill/json_tabular.go`, `json_tabular_test.go`
  - Detail: Lightweight stream-based JSON pruner and Tabular Schema-Lifter. Converts homogeneous arrays of objects into compact Markdown tables.
  - Acceptance: 40–75% token reduction on raw REST API dumps without loss of non-null business data.

- [x] **TSK-006**: Implement Cross-Turn Stale Read Deduplication (Dedup-Squoze).
  - Requirement: FR-5, AC-5.1.1, AC-5.1.2
  - Deliverables: `squoze/internal/distill/dedup.go`, `dedup_test.go`
  - Detail: Track content hashes of large file-read blocks across conversation history. Replace obsolete duplicate reads in earlier turns with backwards reference markers.
  - Acceptance: Active turn retains full file content; earlier redundant file copies replaced with references.

---

## Phase 3 — Quality Guardrails & Cache Safety

- [x] **TSK-007**: Integrate Never-Elide 2.0 and Error Signal Preservation.
  - Requirement: FR-6, AC-6.1.1
  - Deliverables: `squoze/internal/compress/compress.go`
  - Detail: Expanded pattern matcher for errors, exceptions, panics, stack traces, and non-zero exit codes.
  - Acceptance: Zero test failures or error lines elided across the full test corpus.

- [x] **TSK-008**: Verify Cache-Stability and Reversibility Store.
  - Requirement: FR-7, AC-7.1.1, AC-7.2
  - Deliverables: `squoze/internal/engine/engine.go`, `squoze.go`
  - Detail: Ensure byte-identical outputs across subsequent turns on unchanged tool blocks. Exposed `ResolveOriginal` for 12-char SHA-256 ref retrieval.
  - Acceptance: `MemoHits` verified; byte stability preserved for upstream prompt caching.

---

## Phase 4 — Benchmarks, Integration & Review

- [x] **TSK-009**: Run full latency & savings benchmark matrix in 2papi.
  - Requirement: NFR-1, NFR-3
  - Deliverables: `squoze/internal/engine/bench_test.go`, micro-benchmarks
  - Detail: Benchmarked multi-message payloads and large blobs. 40-message multi-turn latency measured at 2.87ms, fast bailout at 1.2µs.
  - Acceptance: Squoze latency drops from 438ms to <2.5–2.8ms; savings reach up to 98.6%.

- [x] **TSK-010**: Wire Squoze v2 telemetry and latency headers into 2papi.
  - Requirement: US-2, US-3
  - Deliverables: `2papi/internal/proxy/proxy.go`, `2papi/internal/proxy/squoze_v2_test.go`
  - Detail: Exposed `X-Gateway-Squoze-Latency-Ms`, `X-Gateway-Squoze-Transforms`, `X-Gateway-Squoze-Memo-Hits`. Verified in end-to-end integration test (0.35ms proxy overhead).
  - Acceptance: Live integration test passes with 0.35ms latency and full tabular lifting verification.

---

## Dependency Graph

```
TSK-001 (Stream Scanner) 
   ↓
TSK-002 (Scanner Tests)
   ↓
TSK-003 (ANSI) ──┬── TSK-004 (Diff) ──┬── TSK-005 (JSON) ──┬── TSK-006 (Dedup)
                 │                    │                    │
                 └────────────────────┼────────────────────┘
                                      ↓
                               TSK-007 (Never-Elide)
                                      ↓
                               TSK-008 (Cache Memo)
                                      ↓
                               TSK-009 (Benchmarking)
                                      ↓
                               TSK-010 (2papi Integration)
```

## Progress

| Task | Description | Status |
|---|---|---|
| TSK-001 | Fast bailout & streaming scanner | Complete |
| TSK-002 | Wire format tests | Complete |
| TSK-003 | ANSI & terminal sanitization | Complete |
| TSK-004 | Diff & lockfile distiller | Complete |
| TSK-005 | JSON structural pruner | Complete |
| TSK-006 | Cross-turn file dedup | Complete |
| TSK-007 | Never-elide 2.0 guardrails | Complete |
| TSK-008 | Cache-stability & reversibility | Complete |
| TSK-009 | Latency & savings benchmarks | Complete |
| TSK-010 | 2papi telemetry & headers | Complete |
