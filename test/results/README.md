# test/results — what each report is, and whether it is backed

Every file here is machine-written by a command in this repo. This table is the
contract: a report with no re-run command, or one whose numbers an audit
disproved, is marked as such rather than quietly left to look authoritative.

Status legend — **backed**: reproducible and the numbers mean what they say ·
**context only**: reproducible, but reads as a signal it cannot support ·
**disproved**: the audit found the numbers unsound, kept as evidence ·
**BLOCKED**: the run could not complete, and says so instead of estimating.

| File | Produced by | Status |
|---|---|---|
| `squoze_quality_report.json` | `go run ./test/squozebench` | **backed** — 15 offline cases, needle recall / idempotency / determinism / prefix stability graded per case; the committed run is squoze **v0.3.0**, the version `go.mod` pins, and the file states it in `squoze_version` |
| `squoze_ab/squoze_quality_report.{base,head}.json` | `test/squozebench/repro/savings_ab.sh` | **backed** — squoze v0.2.0 (11 pass / 3 fail) vs. the tree that became v0.3.0 (14 pass / 0 fail), compared by `repro/cmp_savings.mjs`, which reads exactly this pair. Both sides report `squoze_version: 0.2.0` because the head tree had not yet bumped the constant; the released v0.3.0 run is the top-level report and is verdict- and savings-identical to the head side |
| `squoze_ab/{base,head}.1..3.json` | same script, `N=3` | **backed** — the three repeats per side the canonical pair is drawn from; they exist so p95 has a spread instead of a single sample, and the spread earns its keep: worst-case p95 is 5.0–6.5 ms in five of the six runs and 14.9 ms in `head.2`, on a case that measured 4.6 ms in the other two — host contention, visible only because the run was repeated |
| `conformance_report.json` | `node test/conformance.mjs` | **backed** — 9 pass / 0 fail / 3 skip on OpenAI wire-shape conformance |
| `provider_probe.json` | `PROBE_KEY=… node test/provider_probe.mjs` | **backed** — which upstream models answer, and whether they report `usage` |
| `crax_probe.json` | `PROBE_KEY=… node test/crax_probe.mjs` | **backed** — model list and context windows as advertised by the provider |
| `gateway_matrix_report.json` | `node test/matrix_compare.mjs` | **backed for overhead drift**, context only for `rps`: absolute throughput moves with host load, so the actionable signal is overhead on a mode whose implementation changed |
| `accuracy_report.json` | `REPEATS=5 … node test/accuracy_suite.mjs` | **BLOCKED** — preflight got HTTP 502 from the upstream. The gates it would have judged (Δaccuracy ≤ 2 pp, savings ≥ 50%, overhead p95 ≤ 15 ms) are unmeasured, and the file says so |
| `swe_bench_report.json` | `node test/swe_bench_suite.mjs` | **disproved** — grading is substring matching, the instance IDs are not upstream instances. See [benchmarks/README.md](../benchmarks/README.md) |
| `terminalbench_aider_report.json` | `node test/terminalbench_aider_suite.mjs` | **disproved** — same |
| `combat_suite_report.json` | `node test/combat_suite.mjs` | **disproved** — reported token counts cannot come from the fixtures (≈1 638 tokens in, 21 521 reported), latency is n=1 per condition |
| `live_oss_report.json` | `node test/live_oss_suite.mjs` | **context only** — one real issue, one run, pattern-matched reply |
| `benchmark_summary.json` | *no producer in the repo* | **disproved** — `usage: null`, empty headers: it measures nothing, because `test/fakeupstream` never emits `usage`. Kept only because [docs/benchmark-audit.md](../../docs/benchmark-audit.md) cites it as the evidence for that finding |

Not tracked, regenerated on demand: `matrix_raw.txt` (raw `wrk` output behind the
matrix report) and `squoze_artifacts/` (1.3 MB of per-case before/after blobs
from squozebench). Both are in `.gitignore`; the reports that summarise them are
here.

The full account of what was wrong with the disproved rows, and how it was
found, is [docs/benchmark-audit.md](../../docs/benchmark-audit.md).
