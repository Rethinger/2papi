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
| `squoze_quality_report.json` | `go run ./test/squozebench` | **backed** — 15 offline cases, needle recall / idempotency / determinism / prefix stability graded per case; the committed run is squoze **v0.4.0**, the version `go.mod` pins, and the file states it in `squoze_version`. 14 pass / 0 fail / 1 known-limit, 97.02% median savings on the 9 cases compression fired on, worst-case engine p95 5.58 ms. Verdict-identical to the v0.3.0 run (p95 5.4 ms), which is what a release that only adds a size bound should look like |
| `squoze_ab/squoze_quality_report.{base,head}.json` | `test/squozebench/repro/savings_ab.sh` | **backed** — squoze v0.2.0 (11 pass / 3 fail) vs. the tree that became v0.3.0 (14 pass / 0 fail), compared by `repro/cmp_savings.mjs`, which reads exactly this pair. Both sides report `squoze_version: 0.2.0` because the head tree had not yet bumped the constant; the released v0.3.0 run is the top-level report and is verdict- and savings-identical to the head side |
| `squoze_ab/{base,head}.1..3.json` | same script, `N=3` | **backed** — the three repeats per side the canonical pair is drawn from; they exist so p95 has a spread instead of a single sample, and the spread earns its keep: worst-case p95 is 5.0–6.5 ms in five of the six runs and 14.9 ms in `head.2`, on a case that measured 4.6 ms in the other two — host contention, visible only because the run was repeated |
| `conformance_report.json` | `node test/conformance.mjs` | **backed** — 9 pass / 0 fail / 3 skip on OpenAI wire-shape conformance |
| `provider_probe.json` | `PROBE_KEY=… node test/provider_probe.mjs` | **backed as an outage record, not as an availability list** — gorouter, 2026-09-04: `/v1/models` answered 200 with an empty list and 0 of 15 probed models completed (fourteen 403, one 503). It records that the provider was unreachable with this key; it establishes nothing about which models work |
| `crax_probe.json` | `PROBE_KEY=… node test/crax_probe.mjs` | **backed for 2026-09-04 only** — 47 models listed and all 47 answered `OK42`. Two facts make it unusable as a current catalogue: every model reported `prompt_tokens: 1016` for a ~6-token prompt, so its `usage` is a placeholder and no token-savings figure may be taken from it; and by 2026-09-05 the catalogue had shrunk to 19 ids with no `claude-*` model at all |
| `provider_probe.crax.json` | `PROBE_KEY=… PROBE_BASE=https://gpt.crax.lol/v1 PROBE_MODELS=… PROBE_OUT=provider_probe.crax.json node test/provider_probe.mjs` | **backed as an outage record** — crax, 2026-09-05: `/v1/models` answered 200 with 19 ids, and 0 of the 15 text models completed (nine 502, six 429). Minutes later the same calls returned 403 `site_locked` with the provider's own text, *Site temporarily locked — please try again later*. Separate file by `PROBE_OUT` on purpose: probing a second provider into `provider_probe.json` would delete the evidence for the first |
| `gateway_matrix_report.json` | `node test/matrix_compare.mjs` | **backed for overhead drift**, context only for `rps`: absolute throughput moves with host load, so the actionable signal is overhead on a mode whose implementation changed |
| `accuracy_report.json` | `REPEATS=3 GATEWAY_URL=… MODEL_SQUOZE=… MODEL_BASELINE=… node test/accuracy_suite.mjs` | **BLOCKED** — re-run 2026-09-05 against a gateway wired to crax with paired aliases (`crax-squoze` / `crax-nosquoze`, same upstream model, differing only in whether squoze runs). Preflight got HTTP 403 `site_locked`. Both providers on hand are down — gorouter 403 since 2026-09-04, crax 502 → 429 → 403 on 2026-09-05 — so Δaccuracy ≤ 2 pp, savings ≥ 50% and overhead p95 ≤ 15 ms stay unmeasured **on a live provider**. What changed is that the gates are now judged rather than printed: `evaluateGates()` resolves each to `pass`/`fail`/`null` and sets `status` to `PASS` / `FAIL` / `PARTIAL` / `INCONCLUSIVE`, instead of the unconditional `COMPLETE` it used to write. That logic is exercised offline — see the row below |
| *(no report)* | `node test/accuracy_gates_selftest.mjs` | **backed** — 11 checks on the exported gate logic, all passing: a run where squoze never fired comes out `INCONCLUSIVE` and not `PASS`, a −3.1 pp accuracy drop fails, an accuracy *gain* never fails, savings of 41% fails, a p95 of 40 ms fails, latency-0 trials from skipped requests do not dilute the overhead p95, errored trials are excluded, and a provider reporting a constant `prompt_tokens` is marked `usable_for_savings: false`. It needs no provider, which is the point |
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
