# Squoze & 2papi Engineering Roadmap (Post-Benchmark Action Plan)

This steering document records the 5 critical architectural enhancements identified from real-world combat benchmarks:

---

## 1. Tool Output Compression in `role: "user"` (Aider & Cursor Ecosystem)
- **Problem**: `stream_scanner.go` currently only targets `role: "tool"`. Frameworks like Aider, Cursor, and Claude Code often wrap tool results (terminal output, git diff) inside `role: "user"` using XML tags like `<tool_output name="...">` or markdown code fences.
- **Solution**: Add an inline parser that identifies `<tool_output>`, `<command_result>`, and ````terminal` fences within `role: "user"` messages. Compress only the machine content with `Terminal-Squoze` and `Diff-Squoze` while strictly preserving human user prompt instructions.
- **Expected Impact**: Extends Squoze distillation coverage to 100% of major coding agent workflows.

---

## 2. Adaptive Thinking-Budget Shaper (Extended Thinking Optimization)
- **Problem**: On models with deep reasoning (Claude Opus 5, OpenAI o3, DeepSeek R1), uncapped thinking budgets lead to 100–160s pauses on straightforward CLI and diagnostic tasks.
- **Solution**: Introduce `thinking_budget: N` in 2papi model configurations. Dynamically shape the `thinking` parameter or inject a concise-reasoning directive into the system prompt for routine bug-fixing workflows.
- **Expected Impact**: Reduces user wait time from 2–3 minutes to 15–30 seconds without sacrificing patch quality.

---

## 3. Prompt-Cache Aware Distillation (Anthropic / DeepSeek Cache Preservation)
- **Problem**: Frontier providers offer a 90% discount on cached prompt prefixes (e.g. Anthropic 5-minute ephemeral cache). Aggressive cross-turn pruning on historical turns can invalidate prefix cache hashes.
- **Solution**: Implement "Cache-Epochs Retention": preserve prefix stability by restricting cross-turn deduplication to stable epoch boundaries (e.g. every 5–10 turns) or compressing only new tail tool results before they freeze into the cached prefix.
- **Expected Impact**: Unlocks simultaneous double savings: 20–40% token volume reduction + 90% prompt cache hit rates.

---

## 4. AST Code Outline Compression (Skeletonizer for Large File Reads)
- **Problem**: Coding agents frequently read 500–1,000 line source files just to inspect interfaces or function signatures. Never-Elide 2.0 safely passes entire files raw.
- **Solution**: For non-target reference files, provide an optional AST skeletonizer that folds function bodies (`def foo(): ...` / `func foo() { ... }`) while keeping signatures, docstrings, and types.
- **Expected Impact**: Additional 30–45% token reduction on repository-level context exploration.

---

## 5. Web Dashboard Savings & ROI Telemetry Widget
- **Problem**: Squoze telemetry is currently visible only via response HTTP headers (`X-Gateway-Saved-Bytes`, `X-Gateway-Squoze-Latency-Ms`).
- **Solution**: Add a real-time ROI widget to `http://localhost:8080/dashboard/`:
  - Total tokens pruned
  - Estimated $$$ saved on upstream billing
  - Cumulative developer time saved
  - Breakdown by noise category (Lockfiles, ANSI, JSON, Duplicate Reads).
