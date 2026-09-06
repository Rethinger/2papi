# Benchmark audit — what the numbers actually support

Run date: 2026-09-04. Spec: [`.kiro/specs/benchmark-integrity`](../.kiro/specs/benchmark-integrity/requirements.md).

Every number below is reproducible with the command next to it. Where something
could not be measured, it says so instead of estimating.

## 1. Audit of the previous "State-of-the-Art Benchmarks" section

The README claimed 100% Pass@1 on SWE-bench Verified, TerminalBench v2.1 and
Aider Polyglot, plus token savings attributed to Squoze. Checking those claims
against the very files they cite (`test/results/*.json`):

| Claim | What the cited data shows |
|---|---|
| Squoze produced the token savings in all 7 benchmark rows | `squoze=false, savedBytes=0` in **5 of 8 runs**. Only scenario 1 and 2 had Squoze fire at all |
| `django__django-16595`: "Squoze Solved It! (+50% Pass@1)" | Squoze was **inactive** in that run (`squoze=false`, `savedBytes=0`), and input tokens went *up* (11 923 → 12 950). The difference is sampling variance between two runs of the same input |
| SWE-bench Verified "PASSED (RESOLVED)" | Grading is `fullResponse.includes(required_patch_subsequence)`. No repository checkout, no patch application, no `FAIL_TO_PASS`/`PASS_TO_PASS` execution |
| "TerminalBench v2.1 `v2_sys_042`", "Aider Polyglot `polyglot_rust_018`" | These instance IDs do not exist in the upstream datasets. The fixtures are hand-written scenarios (`test/benchmarks/*.json`) |
| Scenario 1: "21 521 → 18 157 tokens" | The fixture is ~1 638 tokens by character count. A reported `prompt_tokens` of 21 521 cannot come from it, and `completion_tokens=3 019` exceeds the request's `max_tokens=1500` |
| Latency deltas ("3.1x faster", "5.08s faster") | n=1 per condition. Observed spread across runs of the same input is 4–164 s |

Root cause of the Squoze-inactive rows: `profile.presets[Claude].MinBytes = 4096`
(squoze v0.2.0, `internal/profile/profile.go`). Every fixture's tool blob is
under 4 KB, so the size gate correctly refused to compress — the benchmark was
measuring nothing.

`docs/benchmarks.md` was already honest about this ("squoze is the most
expensive pass by far… it stays experimental and config-only for good reason"),
so the README and the benchmark doc contradicted each other.

## 2. Provider availability — the live layer is blocked

```sh
PROBE_KEY=... node test/provider_probe.mjs
```

`test/results/provider_probe.json`: the configured upstream (`gorouter.app`)
serves **zero** working models for this key. `claude-opus-5` returns a JSON
`503 "No available channel for model claude-opus-5 under group default"`; every
other candidate returns a bare `403` from the upstream (not Cloudflare — the
body is empty with `Content-Type: application/octet-stream`), and `GET /v1/models`
returns an empty list.

Consequence: no accuracy claim about Squoze can be made right now, because
accuracy requires a model. `test/accuracy_suite.mjs` therefore reports
`status: "BLOCKED"` rather than producing a number.

## 3. Squoze compression quality — offline, deterministic

This is the layer that *can* be measured without a provider: the optimizer is
deterministic by design, so whether it preserved a fact is decidable.

```sh
docker run --rm -v "$PWD:/src" -w /src golang:1.23 go run ./test/squozebench
node test/tokenscore.mjs
```

15 cases across 7 classes → `test/results/squoze_quality_report.json`, which
now holds the v0.3.0 run. **11 pass · 3 fail · 1 known-limit** is the squoze
v0.2.0 result this audit describes and was written for; it is kept as
`test/results/squoze_ab/base.{1,2,3}.json`.

> **Fixed in squoze v0.3.0, released 2026-09-05, and pinned by `go.mod` since:
> 14 pass · 0 fail · 1 known-limit**, median savings 97.02% when compression
> fires, worst p95 engine latency 5.4 ms, no broken prefixes. Every finding
> below is fixed there. The numbers in the rest of this section are the v0.2.0
> measurements and are left as they were, because the point of the audit is what
> the release did. One corpus entry was also wrong rather than merely failing:
> `json_api_list_800_rows` asserted both `ExpectEither` and a JSON format
> contract, and lifting 800 rows into a Markdown table cannot satisfy the
> second — the contract was dropped and `TestJSONEnvelopeLoss` now carries the
> property it was reaching for. The v0.3.0 numbers come from the command above,
> unmodified; the v0.2.0 side is reproducible from
> `test/results/squoze_ab/base.{1,2,3}.json` and
> `test/squozebench/repro/README.md`.

### What works, and works well

| Case | Byte savings | Token savings | Needle recall |
|---|---|---|---|
| go test, 3 000 lines, 2 FAILs buried mid-stream | 97.9% | 97.8% | 100% |
| pytest, 400 passing + trailing traceback | 87.4% | 86.9% | 100% |
| k8s logs, 1 200 lines, 2 critical lines | 96.1% | 96.1% | 100% |
| pnpm ANSI progress spam | 99.9% | 99.9% | 100% |
| unified diff + 300-line lockfile hunk | 98.9% | 98.9% | 100% |
| tool output inside a `user` message fence | 97.9% | 97.8% | 100% |

Engine latency p50 0.13–9.35 ms, worst p95 9.85 ms across the corpus.

Two things worth stating plainly:

- **Byte savings and token savings agree** within 3 pp on every case (largest
  divergence: JSON, 24.4% bytes vs 27.7% tokens). So `X-Gateway-Saved-Bytes` is
  an honest proxy for billing impact on this corpus.
- **When Squoze fires, it saves 87–99%, not 5–15%.** The README's small numbers
  came from fixtures too small to trigger it. The capability is materially
  better than was advertised; the safety is worse.

Size gates behave correctly: 3 KB blob under the Claude 4096 gate passes
through untouched; 6 KB crosses it and compresses.

### Contract violations found

**(a) Source code dense in test markers is elided as machine output.**

`router.Classify` returns `KindTestOutput` at `testScore >= 3`, counting
substrings including `assert `, `FAILED`, `PASSED`, `=== RUN`. Source code
containing enough of them is compressed:

| Case | Before | After | Result |
|---|---|---|---|
| Go file with 120 `FAILED`-bearing error strings | 29 159 B | 4 245 B | 864 lines elided; output is **syntactically broken Go** (functions cut mid-body) |
| Ordinary-looking test-helper file | 6 184 tok | 450 tok | 92.5% removed |

`internal/router/router.go`'s own doc comment says "Prose, code and JSON blobs
are routed but NOT mutated". That contract does not hold.

**Calibration — this is a latent hazard, not an active outage.** Feeding real
files from this repository through the engine:

```sh
docker run --rm -v "$PWD:/src" -w /src golang:1.23 \
  go test -v -run 'TestRealRepoFiles|TestClassificationMargin' ./test/squozebench/
```

- **1 of 93** real source files ≥4 KB was elided on the v0.2.0 pin — and it is
  this suite's own `corpus.go`, which deliberately contains test-output samples.
  Re-run against the v0.3.0 pin the same scan checks **97** files and elides
  **none**, `corpus.go` included: that release added a code-opener guard
  (`hasCodeOpener` + `codeMarkers >= 2` → `KindCode`, ahead of the test-output
  branch and absent in v0.2.0) precisely for fixture generators and golden files.
- **0 of 65** real `_test.go` / `.test.ts` files score above the threshold.
  They all score **1** against a threshold of **3**, because real Go tests use
  `t.Fatalf` / `require.Equal`, not the literal strings `FAILED` / `PASSED` /
  `assert `. The scan skips its own mirror file, which holds those markers as
  list literals: it sat under the 4 KB floor when this audit was written and is
  excluded by name since, so the number keeps meaning "real test files".

So the realistic exposure was narrow even then: files that *store* test output
as data (fixtures, golden files, corpora, log-parsing test data). The mechanism
was real, but ordinary code was not being truncated — and on v0.3.0 not even the
fixture generator is.

`TestRealRepoFilesAreNotElided` **passes** on both pins. Its `corpus.go`
exemption (a log line, not a failure — the file embeds machine output by
construction) is what v0.2.0 needed and v0.3.0 no longer exercises; the test
keeps it, because the guard that made it unnecessary is one release old. It
fails if any file under `internal/`, `cmd/`, or ordinary test code starts
getting elided, which is the regression worth catching.

**(b) JSON silently becomes a Markdown table, and envelope fields are lost.**

`distill.tryTabularLifting` converts a homogeneous array of objects into a
Markdown table. On an 800-row paginated API response, `has_more: true` and
`object: "list"` are dropped — a model reading the distilled form cannot tell
that more pages exist. The output is no longer JSON, so any consumer that parses
tool results breaks.

**(c) Column order is non-deterministic — this breaks the cache-safe contract.**

```
NON-DETERMINISTIC: 3 distinct column orders across 12 fresh engines
  variant (n=9): | id | status | amount_usd | note |
  variant (n=1): | amount_usd | note | id | status |
  variant (n=2): | note | id | status | amount_usd |
```

The number of variants is itself random, so it differs between runs (a later run
of the same test produced 4 variants over the same 12 engines). What reproduces
is the non-determinism, not the count — treat any variant count above 1 as the
failure.

`tryTabularLifting` builds `colKeys` by ranging over a Go map, whose iteration
order is randomized. squoze's stated contract is "identical original bytes
always produce byte-identical output, which keeps provider prompt caches
stable". With a JSON tool result in the body, the prompt cache can never hit.
One-line fix: `sort.Strings(colKeys)`.

**(d) Cross-turn dedup resends an earlier turn in full and breaks the cache prefix.**

Measured on a 4-turn session where turn 3 re-reads the file from turn 1:

```
turn 1→2: 2/2 shared messages stable, 100.0% of shared bytes (stable)
turn 2→3: 1/3 shared messages stable, 0.8%  (BROKEN at message 1)
  → message 1 (role=tool) was rewritten: 4 245 bytes -> 29 159 bytes
turn 3→4: 4/4 shared messages stable, 100.0% (stable)
```

Turn 2 had that message correctly elided to 4 245 B. Turn 3 sends it back at its
**full 29 159 B** — the request grows by ~25 KB *and* the cache prefix dies at
message 1, invalidating every cached token after it.

Mechanism (`internal/engine/stream_scanner.go`, v0.2.0): `DeduplicateHistoricalReads`
replaces the earlier copy with a short marker. `distillText()` returns `""` for
that marker (under its 64-byte floor), so the fallback restores `out = t.content`
— and the next guard is `out != "" && out != t.content`, which is now false. The
replacement is dropped and the message keeps its original bytes. The Anthropic
path has the same shape.

Note the flow matters: when the client echoes back what it was *sent* (already
elided), behaviour is correct and stable. The bug needs a client that resends
pristine originals each turn — which is what most agent frameworks do, since they
keep their own conversation history.

**(e) Known algorithm limit, documented rather than hidden.**

`compress.Params.MaxKept = 50` caps rescued middle error lines. A run with 200
distinct failures keeps 16 of them: **recall 8%**. That is a deliberate bound,
but a 200-failure test run is not exotic, and the elision marker does not say
that failures were dropped.

## 4. Gateway overhead matrix

```sh
docker compose --profile bench run --rm bench-matrix > test/results/matrix_raw.txt
node test/matrix_compare.mjs test/results/matrix_raw.txt
```

→ `test/results/gateway_matrix_report.json`.

Absolute throughput on this host came in at roughly half the values in
`docs/benchmarks.md`, and two runs of the same configuration differed
substantially (squoze/huge: 74.7 ms then 146.6 ms). This host was running Docker
Desktop plus other work, so **absolute numbers from these tables should not be
quoted**; the relative ordering of modes reproduced consistently.

One robust finding survives the noise:

| mode, 633 KiB payload | documented then | measured 2026-09-04 (2 runs) |
|---|---|---|
| squoze (exclusive) | 438.56 ms | **74.7 ms**, **146.6 ms** |

Both runs are far below the documented figure, so `docs/benchmarks.md`'s squoze
number is stale — v0.2.0's fast bailout (`len(body) < 256 || !hasCandidate`)
landed after it was written. Squoze is no longer the most expensive pass; on the
huge profile it is now cheaper than RTK and caveman.

**Resolved 2026-09-06 (TSK-018).** The matrix was re-measured on squoze v0.4.0,
with the pin verified on the binary that actually served the requests (`docker cp`
the gateway out of the bench image, then `go version -m`). The comparator no
longer diffs absolute milliseconds at all: every mode is restated as its distance
from the `baseline (off)` row of the *same* run, and per-mode drift is suppressed
unless the measured baseline throughput lands within 0.7-1.4x of the recorded one
— the contended run above, at 0.55x (large) and 0.38x (huge), is refused by that
gate rather than published as 25 regressions. The 438.56 ms row was deleted from
`docs/benchmarks.md` rather than annotated: it was measured on v0.2.0 and reported
`applied = squoze=false`, i.e. the full cost of deciding *not* to compress. On
v0.4.0 the pass fires, at **8.14 ms** on 96.9 KiB and **74.56 ms** on 633.4 KiB,
both with `squoze=true`. The gate is itself tested offline —
`node test/matrix_gate_selftest.mjs`, 9 checks, no Docker and no provider.

## 5. OpenAI wire-protocol conformance

```sh
docker compose run --rm --no-deps -e GATEWAY_URL=http://gateway-bench:8080 \
  -e GATEWAY_KEY=sk-gateway-dev -e GATEWAY_MODEL=bench-model \
  bench-runner node conformance.mjs
```

→ `test/results/conformance_report.json`: **9 pass · 0 fail · 3 inconclusive.**

Passing: `/healthz` and `/readyz` unauthenticated; `/v1/models` returns a proper
OpenAI list; SSE framing terminates with `data: [DONE]`; frames are well-formed
JSON the gateway does not corrupt; unknown key → `401` with a proper error
object; unknown model → `404` with one; optimizer echo headers present and
`X-Gateway-Saved-Bytes=53 271` on a noisy body.

The 3 inconclusive checks are attributable to the test fixture, not the gateway:
`test/fakeupstream/main.go` always answers with SSE regardless of `stream`, emits
only `choices[].delta.content` (no `object`, `id`, or `finish_reason`), and
**never emits `usage`**. That last one is why `test/results/benchmark_summary.json`
has `usage: null` — token savings are structurally unmeasurable through this
fixture.

## 6. Gaps this audit could not close

- **SWE-bench Verified, properly.** Requires per-instance Docker images and
  `FAIL_TO_PASS`/`PASS_TO_PASS` execution (tens of GB). Until that runs, no
  SWE-bench number should appear anywhere in this repo.
- **Δaccuracy under compression.** Squoze's own `docs/eval-protocol.md` names
  LoCoMo / RULER / BFCL v3 with gates (Δaccuracy ≤ 2 pp, savings ≥ 50%,
  overhead < 15 ms p95) and marks Level 2 unchecked. It is still unchecked, now
  blocked on provider availability rather than on tooling —
  `test/accuracy_suite.mjs` is ready and refuses to run below k=3.
- **Clean-host overhead numbers.** Needs a quiet machine.

## 7. Recommended fixes, in impact order

Status as of 2026-09-05. Rows 1–5 are squoze-side and all landed in v0.3.0,
which `go.mod` pins; each is guarded by a named test in `test/squozebench/` so a
regression is a red suite, not a rediscovered audit finding.

| # | Fix | Where | Effort | Status |
|---|---|---|---|---|
| 1 | `sort.Strings(colKeys)` — restore determinism | squoze `internal/distill/json_tabular.go` | one line | **done in v0.3.0** — `sort.Strings` on both key paths; `TestJSONTabularDeterminism` green |
| 2 | Fix the dedup guard so the marker is applied instead of dropped | squoze `internal/engine/stream_scanner.go` (both paths) | small | **done in v0.3.0** — `TestDedupReExpandsHistory` green, prefixes stable on both model families |
| 3 | Preserve JSON envelope fields, or emit the table *alongside* a kept envelope | squoze `internal/distill/json_tabular.go` | small | **done in v0.3.0** — scalar siblings hoisted into the table headline; `TestJSONEnvelopeLoss` green |
| 4 | Raise the code-vs-test-output discrimination: require line-start anchoring for `=== RUN`/`--- FAIL`, and reject blobs that parse as source (brace balance, `^func`/`^def`) | squoze `internal/router/router.go` | medium | **done in v0.3.0** — `hasCodeOpener` + `codeMarkers` ahead of the test branch, crash markers line-anchored; 0 of 97 real files elided (§3 calibration) |
| 5 | State in the elision marker how many error lines were dropped when `MaxKept` is hit | squoze `internal/compress/compress.go` | small | **done in v0.3.0** — marker carries `· N more failure lines over cap=…` |
| 6 | Make the fake upstream OpenAI-faithful (emit `usage`, `object`, `id`, `finish_reason`; honour non-stream requests) so conformance and token measurements are possible offline | `test/fakeupstream/main.go` | medium | **open** — the codex path emits `usage`, `/v1/chat/completions` still answers with bare `{"choices":[{"delta":…}]}` SSE regardless of `stream`. This is why `benchmark_summary.json` reads `usage: null` |
| 7 | Log the upstream status and error message when all attempts fail — currently a `503 "no available channel"` becomes a bare `502` with nothing on stdout | `internal/proxy/proxy.go` | small | **open** — the final path is still `Error(w, http.StatusBadGateway, "all upstream attempts failed")` with no upstream status or body retained |
| 8 | Refresh the squoze row in `docs/benchmarks.md`; it is stale by ~3–6× | `docs/benchmarks.md` | docs | **done 2026-09-06** — the page was re-measured on v0.4.0 and rewritten: three full 14-mode tables, the 438 ms row deleted (v0.2.0, `squoze=false`), hardware and re-run command on every own figure, vendor claims separated into a `kind`-labelled table, and the design-target table dropped. Row 7 stays open: that is `internal/proxy/proxy.go`, not docs |

## 8. How to re-run everything

```sh
# offline: squoze compression quality + contract tests (no keys, no network)
docker run --rm -v "$PWD:/src" -w /src golang:1.23 go run ./test/squozebench
docker run --rm -v "$PWD:/src" -w /src golang:1.23 go test -v ./test/squozebench/
npm install --no-save gpt-tokenizer && node test/tokenscore.mjs

# offline: gateway overhead matrix + wire conformance
docker compose --profile bench up -d --build fake-upstream gateway-bench
docker compose --profile bench run --rm bench-matrix > test/results/matrix_raw.txt
node test/matrix_compare.mjs test/results/matrix_raw.txt
docker compose run --rm --no-deps -e GATEWAY_URL=http://gateway-bench:8080 \
  -e GATEWAY_KEY=sk-gateway-dev -e GATEWAY_MODEL=bench-model \
  bench-runner node conformance.mjs

# live (needs a provider that actually serves the model)
PROBE_KEY=$UPSTREAM_KEY node test/provider_probe.mjs
REPEATS=5 GATEWAY_URL=http://127.0.0.1:8989 node test/accuracy_suite.mjs
```

### Expected result of `go test ./test/squozebench/`

Green, no skips. Three of the tests — `TestJSONTabularDeterminism`,
`TestJSONEnvelopeLoss`, `TestDedupReExpandsHistory` — are regression pins on the
three contracts `github.com/Rethinger/squoze v0.2.0` violated. While no tag
carried the fixes they could only fail against the `go.mod` pin, and a
`go test ./...` that is red on every commit until an unrelated repository cuts a
release is a signal nobody reads — so for one day they skipped unless
`SQUOZE_CONTRACT_PINS=1` was set, with a non-blocking CI job keeping the failures
visible. squoze v0.3.0 (2026-09-05) fixes all three; `go.mod` pins it, the gate
and the extra CI job are deleted, and the three now run unconditionally, where
they protect a fixed contract instead of documenting a broken one.

| Test | Pins finding | Fixed in squoze v0.3.0 by |
|---|---|---|
| `TestJSONEnvelopeLoss` | (b) JSON → Markdown table, envelope fields dropped | `hoistConstantColumns` + envelope in the headline (`internal/distill/json_tabular.go`) |
| `TestJSONTabularDeterminism` | (c) random column order | column order taken from document order, `sort.Strings` only as fallback |
| `TestDedupReExpandsHistory` | (d) earlier turn re-expanded, cache prefix dead | `toolTarget.orig` + inverted dedup direction (`internal/distill/dedup.go`) |

Both assertions in `TestJSONEnvelopeLoss` were rewritten during that work: it
used to require JSON quoting around `"object"`, which is satisfiable only by
declining to compress at all, and now requires the envelope *fields* to survive
in whatever form the output takes. Same for the table headline comparison in
`internal/proxy/squoze_v2_test.go`, now matched by prefix and body instead of
byte-for-byte.

To see the three green before there is a tag, point the module at the working
tree — this is exactly what the A/B harness does, through an alternate module
file, so `go.mod` and `go.sum` are never touched and nothing needs restoring
even if the run is interrupted:

```sh
MSYS_NO_PATHCONV=1 docker run --rm -v "$PWD:/w" -v "/abs/path/to/squoze:/squoze" golang:1.23 sh /w/test/squozebench/repro/savings_ab.sh
```

Passing today regardless of which squoze is linked: `TestIdempotencyAcrossCorpus`,
`TestRealRepoFilesAreNotElided`, `TestClassificationMarginOnRealTestFiles`,
`TestDedupDiagnostic`, `TestDedupOnPreCompressedHistory`. A failure in any of
*those* is a real regression, not a known bug.
