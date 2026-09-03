# Requirements — Thinking Budget & User Tool Squoze

## Overview
Implementation of two critical benchmark-proven enhancements for `2papi` and `squoze`:
1. **Adaptive Thinking Budget (`thinking_budget`) in `2papi`**: Caps runaway reasoning tokens on frontier models (Claude Opus 5, o-series, DeepSeek R1) from 160+ seconds down to 15–30 seconds.
2. **`role: "user"` Machine Tool-Output Distillation in `squoze`**: Extends Squoze compression to coding agents (Aider, Cursor, Cline, OpenCode) that encapsulate command and test outputs in `role: "user"` via XML wrappers (`<tool_output>`) and fenced code blocks (````terminal`, ````diff`), without modifying human prompt text.

---

## Functional Requirements

### FR-1: Thinking Budget Control in 2papi
- The gateway SHALL support `thinking_budget` in model configuration and via the `X-Gateway-Thinking-Budget` HTTP request header.
- When `thinking_budget > 0`:
  - For Anthropic/Bedrock: Ensure `thinking: { type: "enabled", budget_tokens: N }` is set in the request payload.
  - Automatically adjust `max_tokens` / `max_completion_tokens` if needed to be $\ge \text{budget\_tokens} + 1024$ to prevent premature truncation during reasoning.
- Priority: MUST
- Acceptance Criteria:
  - **AC-1.1**: Models configured with `thinking_budget: 2048` emit the thinking block upstream.
  - **AC-1.2**: Header `X-Gateway-Thinking-Budget: 1024` overrides the model default.

### FR-2: Machine Tool Output Distillation in `role: "user"` for Squoze
- Squoze engine SHALL detect machine-generated tool outputs embedded inside `role: "user"` messages across OpenAI and Anthropic formats:
  - XML tags: `<tool_output>`, `<command_result>`, `<file_content>`
  - Fenced code blocks: ````terminal`, ````diff` (specifically lockfile diffs or compiler traces > 30 lines)
- Squoze engine SHALL distill ONLY the machine content inside the wrappers using `Terminal-Squoze` and `Diff-Squoze`.
- Priority: MUST
- Acceptance Criteria:
  - **AC-2.1**: Human instructions before and after the machine tags are preserved verbatim.
  - **AC-2.2**: Pure human prompts (e.g. "How does quicksort work?") are completely untouched.
  - **AC-2.3**: Squoze reports `BlocksSqueezed > 0` and logs saved bytes.

---

## Non-Functional Requirements

### NFR-1: Zero-Copy / Sub-Millisecond Latency
- Processing of `role: "user"` blocks must add $< 0.5$ ms of latency overhead.

### NFR-2: Standalone Squoze Repository Parity
- All engine improvements must exist in `squoze/internal/engine` so `squoze` remains fully operational as an independent library.
