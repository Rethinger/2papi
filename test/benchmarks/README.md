# test/benchmarks — replay fixtures

Nine hand-written JSON fixtures. Each one is a complete chat request (system
instruction + messages) plus a `ground_truth` block, replayed through a running
gateway by the harnesses in `test/`.

## What these are not

**They are not instances of the upstream datasets their names resemble.**
`swe_bench_django_16595.json` and `swe_bench_flask_5014.json` are shaped like
SWE-bench Verified IDs, `aider_polyglot_rust_patch.json` like an Aider Polyglot
task, `terminalbench_oom_crash.json` like a TerminalBench one — but the content
was written here, by hand, and the IDs do not resolve in those datasets. Only
the two `live_issue_*.json` fixtures point at something real, and what is real
about them is the linked GitHub issue, not the fixture.

**Grading is substring matching.** `test/swe_bench_suite.mjs:108-113` decides
"resolved" with `fullResponse.includes(gt.required_patch_subsequence)` plus a
count of `gt.patterns` found in the reply. There is no repository checkout, no
patch application and no `FAIL_TO_PASS` / `PASS_TO_PASS` execution — which is
what upstream SWE-bench actually requires. A `PASSED (RESOLVED)` line from these
harnesses means "the reply contained the expected strings", nothing stronger.

So: **numbers produced from these fixtures are not comparable to published
SWE-bench, Aider Polyglot or TerminalBench scores, and must not be reported as
if they were.** They were, in an earlier revision of the root README; see
[docs/benchmark-audit.md](../../docs/benchmark-audit.md) for what that produced
and how it was found.

## What they are good for

Deterministic replay of realistic, agent-shaped traffic through the gateway.
The payloads carry the things synthetic load tests miss — multi-turn histories,
tool blocks, large diffs, thinking budgets — so they exercise routing, adapter
translation, streaming and squoze activation on bodies that look like real
agent work. That is a fixture set for integration behaviour, not a leaderboard.

One caveat worth keeping in mind when reading squoze savings from them: every
fixture's tool blob is under 4 KB, and the Claude preset's `MinBytes` gate is
4096, so squoze correctly declines to compress most of them. A row showing
`savedBytes=0` is the gate working, not a bug.

## Files

| Fixture | Replayed by | Ground truth |
|---|---|---|
| `scenario1_deadlock.json` | `combat_suite.mjs`, `accuracy_suite.mjs` | `patterns` in the reply |
| `scenario2_monorepo.json` | `combat_suite.mjs`, `accuracy_suite.mjs` | `patterns` |
| `scenario3_multiturn.json` | `combat_suite.mjs`, `accuracy_suite.mjs` | `patterns` |
| `swe_bench_django_16595.json` | `swe_bench_suite.mjs`, `accuracy_suite.mjs` | `file`, `patterns`, `required_patch_subsequence` |
| `swe_bench_flask_5014.json` | `swe_bench_suite.mjs`, `accuracy_suite.mjs` | same |
| `aider_polyglot_rust_patch.json` | `terminalbench_aider_suite.mjs`, `accuracy_suite.mjs` | `file`, `patterns` |
| `terminalbench_oom_crash.json` | `terminalbench_aider_suite.mjs`, `accuracy_suite.mjs` | `file`, `patterns` |
| `live_issue_chi_641.json` | `live_oss_suite.mjs` | `target_file`, `required_patterns` |
| `live_issue_rich_4208.json` | — (fixture only) | `target_file`, `required_patterns` |

## Running

Every harness needs a gateway and a key from the environment; none of them
carry credentials.

```sh
GATEWAY_URL=http://127.0.0.1:8989 GATEWAY_KEY=$KEY node test/swe_bench_suite.mjs
REPEATS=5 GATEWAY_URL=http://127.0.0.1:8989 node test/accuracy_suite.mjs
```

Reports land in `test/results/`; see [../results/README.md](../results/README.md)
for which of those files is backed by what.
