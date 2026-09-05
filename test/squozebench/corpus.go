package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Expectation states what the compressor is contractually supposed to do with
// a case. Both directions matter: a compressor that squeezes source code is
// broken in a way that a savings-only benchmark would report as a win.
type Expectation int

const (
	// ExpectSqueeze — machine output above the family size gate: must shrink.
	ExpectSqueeze Expectation = iota
	// ExpectUntouched — prose, code, or below-gate input: must pass through
	// byte-identical.
	ExpectUntouched
	// ExpectEither — legitimately ambiguous; report what happened, do not
	// grade it.
	ExpectEither
)

func (e Expectation) String() string {
	switch e {
	case ExpectSqueeze:
		return "squeeze"
	case ExpectUntouched:
		return "untouched"
	default:
		return "either"
	}
}

// FormatContract names a structural invariant the output must satisfy.
type FormatContract int

const (
	FormatNone FormatContract = iota
	FormatJSON                // output must remain parseable JSON
	FormatDiff                // output must remain an applicable unified diff
)

// Case is one corpus entry.
type Case struct {
	Name  string
	Class string // corpus class from design.md
	Model string // drives profile.Detect → size gate and aggressiveness
	Notes string

	// Content is the tool-output blob under test.
	Content string
	// MustKeep are facts that the never-elide contract requires to survive
	// verbatim.
	MustKeep []string
	// Expect is the contractual outcome.
	Expect Expectation
	// Format is the structural invariant, if any.
	Format FormatContract
	// KnownLimit marks cases that are expected to lose needles because they
	// exceed a documented algorithm bound (e.g. MaxKept=50).
	KnownLimit string
	// Role places the blob in a tool message (default) or a user message.
	Role string
	// UserWrapper, when set, wraps Content for the user-message path
	// (e.g. "tool_output" → <tool_output>...</tool_output>).
	UserWrapper string
}

// bodyFor builds the OpenAI-chat request body carrying this case's content.
func (c Case) bodyFor() []byte {
	role := c.Role
	if role == "" {
		role = "tool"
	}
	content := c.Content
	if role == "user" && c.UserWrapper != "" {
		switch c.UserWrapper {
		case "fence-terminal":
			content = "Here is what the test run printed. Fix the failure.\n\n```terminal\n" + content + "\n```\n\nExplain the root cause first."
		default:
			content = "Here is the captured output. Diagnose it.\n\n<" + c.UserWrapper + ">\n" + content +
				"\n</" + c.UserWrapper + ">\n\nDo not skip the stack trace."
		}
	}
	msgs := []map[string]string{
		{"role": "user", "content": "run the suite and fix what fails"},
	}
	if role == "tool" {
		msgs = append(msgs, map[string]string{"role": "tool", "content": content})
	} else {
		msgs = append(msgs, map[string]string{"role": "user", "content": content})
	}
	body, err := json.Marshal(map[string]any{"model": c.Model, "messages": msgs})
	if err != nil {
		panic(err)
	}
	return body
}

// ---------- generators ----------

// goTestOutput builds go-test output with FAIL lines buried mid-stream, so
// they can only survive via the error-rescue path rather than by landing in
// the head/tail windows.
func goTestOutput(lines int, failAt []int) string {
	var b strings.Builder
	b.WriteString("$ go test ./... -count=1 -race\n")
	failSet := map[int]bool{}
	for _, f := range failAt {
		failSet[f] = true
	}
	fi := 0
	for i := 0; i < lines; i++ {
		switch {
		case failSet[i]:
			fi++
			fmt.Fprintf(&b, "--- FAIL: TestOrderTotal_%d (0.0%ds)\n", fi, i%9)
			fmt.Fprintf(&b, "    order_test.go:%d: total mismatch: want %d got %d\n", 100+i, 4200+fi, 420+fi)
		case i%11 == 0:
			fmt.Fprintf(&b, "ok  \tgithub.com/acme/svc/internal/orders\t0.%02ds\n", i%99)
		case i%5 == 0:
			fmt.Fprintf(&b, "=== RUN   TestOrderTotal_%d\n", i)
		default:
			fmt.Fprintf(&b, "    orders_test.go:%d: verbose assertion padding padding padding ok\n", i)
		}
	}
	b.WriteString("FAIL\ngithub.com/acme/svc/internal/orders\t3.417s\nFAIL\n")
	return b.String()
}

// pytestOutput builds pytest output ending in a real traceback.
func pytestOutput(passing int) string {
	var b strings.Builder
	b.WriteString("============================= test session starts ==============================\n")
	b.WriteString("platform linux -- Python 3.12.4, pytest-8.3.2, pluggy-1.5.0\n")
	b.WriteString("rootdir: /workspace/svc\ncollected 412 items\n\n")
	for i := 0; i < passing; i++ {
		fmt.Fprintf(&b, "tests/test_api.py::test_endpoint_%03d PASSED                            [ %2d%%]\n", i, (i*100)/passing)
	}
	b.WriteString("tests/test_billing.py::test_refund_idempotency FAILED                    [100%]\n\n")
	b.WriteString("=================================== FAILURES ===================================\n")
	b.WriteString("___________________________ test_refund_idempotency ____________________________\n\n")
	b.WriteString("    def test_refund_idempotency():\n")
	b.WriteString("        client = BillingClient(sandbox=True)\n")
	b.WriteString("        first = client.refund(charge_id=\"ch_9f2\", amount=4200)\n")
	b.WriteString("        second = client.refund(charge_id=\"ch_9f2\", amount=4200)\n")
	b.WriteString(">       assert first.id == second.id\n")
	b.WriteString("E       AssertionError: refund amount mismatch: 420 != 4200\n\n")
	b.WriteString("tests/test_billing.py:88: AssertionError\n")
	b.WriteString("=========================== short test summary info ============================\n")
	b.WriteString("FAILED tests/test_billing.py::test_refund_idempotency - AssertionError: refund amount mismatch: 420 != 4200\n")
	b.WriteString("1 failed, 411 passed in 42.19s\n")
	return b.String()
}

// k8sLogs builds timestamped leveled logs with rare critical lines. The two
// critical lines are placed proportionally so they exist at every corpus size.
func k8sLogs(lines int) string {
	var b strings.Builder
	deadlockAt := lines / 4
	fatalAt := (lines * 3) / 4
	for i := 0; i < lines; i++ {
		lvl := "INFO"
		switch {
		case i == deadlockAt:
			lvl = "ERROR"
		case i == fatalAt:
			lvl = "FATAL"
		case i%97 == 0:
			lvl = "WARN"
		}
		fmt.Fprintf(&b, "2026-09-04T07:%02d:%02dZ %s payment-worker reconcile batch=%d latency=%dms\n",
			(i/60)%60, i%60, lvl, i, 8+i%40)
		if i == deadlockAt {
			b.WriteString("2026-09-04T07:05:17Z ERROR payment-worker pq: deadlock detected on relation ledger_entries\n")
		}
		if i == fatalAt {
			b.WriteString("2026-09-04T07:15:04Z FATAL payment-worker exiting: context deadline exceeded after 3 retries\n")
		}
	}
	return b.String()
}

// ansiProgress builds npm/pnpm-style output full of ANSI escapes and CR
// progress redraws — the class terminal sanitation is meant to collapse.
func ansiProgress(steps int) string {
	var b strings.Builder
	b.WriteString("\x1b[1m$ pnpm install --frozen-lockfile\x1b[0m\n")
	for i := 0; i < steps; i++ {
		fmt.Fprintf(&b, "\x1b[2K\r\x1b[36mProgress\x1b[0m: resolved \x1b[1m%d\x1b[0m, reused \x1b[1m%d\x1b[0m, downloaded \x1b[1m%d\x1b[0m, added %d",
			i*7, i*5, i*2, i)
	}
	b.WriteString("\n\x1b[31mERR_PNPM_PEER_DEP_ISSUES\x1b[0m Unmet peer dependencies\n")
	b.WriteString("\x1b[31mERROR\x1b[0m  react@18.3.1: peer react-dom@^19 required\n")
	b.WriteString("Done in 41.2s\n")
	return b.String()
}

// realisticTestHelper is the non-adversarial counterpart to the trap below:
// an ordinary Go test-helper file of the kind a coding agent reads dozens of
// times per session. Nothing here is contrived — assertion helpers naturally
// contain the substrings the router scores on.
func realisticTestHelper() string {
	var b strings.Builder
	b.WriteString(`package testutil

// Package testutil holds the shared assertion helpers used across the order
// and billing suites. This is ordinary source code, not test output.

import "testing"

// AssertEqual fails the test when got and want differ.
func AssertEqual[T comparable](t *testing.T, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("assert failed: want %v got %v", want, got)
	}
}

// AssertNoError fails the test when err is non-nil.
func AssertNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("assert failed: unexpected error: %v", err)
	}
}

// RequireStatus asserts an HTTP status code and reports FAILED with the body.
func RequireStatus(t *testing.T, got, want int, body string) {
	t.Helper()
	if got != want {
		t.Fatalf("FAILED status: want %d got %d body=%q", want, got, body)
	}
}

// OrderFixture is one table-driven case for the order total suite.
type OrderFixture struct {
	Name     string
	Qty      int
	Unit     int
	Expected int
	Comment  string
}
`)
	for i := 0; i < 90; i++ {
		fmt.Fprintf(&b, `
// Fixture%d is one table-driven case for the order total suite.
var Fixture%d = OrderFixture{
	Name:     "order total case %d",
	Qty:      %d,
	Unit:     %d,
	Expected: %d,
	Comment:  "verified against the ledger; assert on Expected",
}
`, i, i, i, i+1, 100+i, (i+1)*(100+i))
	}
	return b.String()
}

// goSourceWithTestWords is the misclassification trap: real source code that
// is dense in the substrings the router uses to detect test output.
func goSourceWithTestWords() string {
	var b strings.Builder
	b.WriteString("package orders\n\nimport (\n\t\"errors\"\n\t\"fmt\"\n)\n\n")
	b.WriteString("// ErrTotalMismatch is returned when the computed total disagrees with\n")
	b.WriteString("// the stored total. Callers assert on this to decide whether to retry.\n")
	b.WriteString("var ErrTotalMismatch = errors.New(\"total mismatch\")\n\n")
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&b, "// assert%d documents the invariant checked by TestOrderTotal_%d.\n", i, i)
		fmt.Fprintf(&b, "func assertInvariant%d(got, want int) error {\n", i)
		b.WriteString("\tif got != want {\n")
		fmt.Fprintf(&b, "\t\treturn fmt.Errorf(\"FAILED invariant %d: want %%d got %%d: %%w\", want, got, ErrTotalMismatch)\n", i)
		b.WriteString("\t}\n\treturn nil\n}\n\n")
	}
	b.WriteString("// PASSED is a sentinel used by the legacy assert helper.\nconst PASSED = \"ok\"\n")
	return b.String()
}

// incidentProse is human writing that mentions errors and failures — must
// never be elided.
func incidentProse() string {
	var b strings.Builder
	b.WriteString("Postmortem: payment-worker outage, 2026-09-01\n\n")
	paras := []string{
		"The incident began when the reconciliation batch started failing with a deadlock error on the ledger_entries table. On-call was paged at 07:05 UTC and acknowledged within two minutes.",
		"Our first hypothesis was wrong. We assumed the ERROR lines in the worker logs pointed at the database, but the failure was upstream: a schema migration had been applied out of order, so the worker held two locks in the opposite sequence from the reconciler.",
		"The customer impact was limited to delayed settlement reports. No payments were lost, and no double refunds were issued, which we confirmed by replaying the ledger from the write-ahead log.",
		"What went well: the circuit breaker tripped before the retry storm could saturate the connection pool. What went badly: our alerting treated FATAL and ERROR identically, so the page did not convey severity.",
		"Action items: enforce migration ordering in CI, split alert routes by level, and add a synthetic reconciliation probe that fails loudly rather than silently.",
	}
	for i := 0; i < 6; i++ {
		for _, p := range paras {
			b.WriteString(p)
			b.WriteString("\n\n")
		}
	}
	return b.String()
}

// jsonAPIResponse builds a large but valid JSON document.
func jsonAPIResponse(items int) string {
	type row struct {
		ID     string  `json:"id"`
		Status string  `json:"status"`
		Amount float64 `json:"amount_usd"`
		Note   string  `json:"note"`
	}
	rows := make([]row, 0, items)
	for i := 0; i < items; i++ {
		st := "settled"
		if i%37 == 0 {
			st = "failed"
		}
		rows = append(rows, row{
			ID:     fmt.Sprintf("ch_%06d", i),
			Status: st,
			Amount: float64(i%9000) / 100,
			Note:   "reconciled against ledger batch 4471 with no discrepancy detected",
		})
	}
	// next_cursor and total_count are the envelope fields whose loss actually
	// changes caller behaviour: without them a reader of the distilled table
	// concludes it has the whole list and stops paginating.
	payload := map[string]any{
		"object":      "list",
		"has_more":    true,
		"total_count": items * 4,
		"next_cursor": "cur_8f21ab",
		"data":        rows,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// unifiedDiff builds a realistic unified diff including a lockfile hunk.
func unifiedDiff(lockLines int) string {
	var b strings.Builder
	b.WriteString("diff --git a/internal/orders/total.go b/internal/orders/total.go\n")
	b.WriteString("index 4f1a2b3..9c8d7e6 100644\n--- a/internal/orders/total.go\n+++ b/internal/orders/total.go\n")
	b.WriteString("@@ -41,7 +41,11 @@ func (o *Order) Total() (int, error) {\n")
	b.WriteString(" \tsubtotal := 0\n \tfor _, li := range o.Lines {\n")
	b.WriteString("-\t\tsubtotal += li.UnitPrice * li.Qty\n")
	b.WriteString("+\t\tif li.Qty < 0 {\n+\t\t\treturn 0, ErrNegativeQty\n+\t\t}\n")
	b.WriteString("+\t\tsubtotal += li.UnitPrice * li.Qty\n")
	b.WriteString(" \t}\n \treturn subtotal, nil\n }\n")
	b.WriteString("diff --git a/pnpm-lock.yaml b/pnpm-lock.yaml\n")
	b.WriteString("index 1111111..2222222 100644\n--- a/pnpm-lock.yaml\n+++ b/pnpm-lock.yaml\n")
	fmt.Fprintf(&b, "@@ -1,%d +1,%d @@\n", lockLines, lockLines)
	for i := 0; i < lockLines; i++ {
		fmt.Fprintf(&b, "-  /@babel/helper-plugin-utils@7.24.%d:\n", i)
		fmt.Fprintf(&b, "+  /@babel/helper-plugin-utils@7.25.%d:\n", i)
		fmt.Fprintf(&b, "     resolution: {integrity: sha512-%040d}\n", i)
	}
	return b.String()
}

// Corpus returns the deterministic case set.
func Corpus() []Case {
	// Two FAILs deep in the middle of a large run.
	bigGoTest := goTestOutput(3000, []int{1137, 2421})
	// 200 distinct failures against MaxKept=50 — the documented bound.
	manyFails := make([]int, 0, 200)
	for i := 0; i < 200; i++ {
		manyFails = append(manyFails, 60+i*12)
	}

	cases := []Case{
		{
			Name:    "go_test_3k_lines_buried_fails",
			Class:   "should-squeeze",
			Model:   "claude-opus-4-5",
			Notes:   "300KB-class go test output, 2 FAILs buried mid-stream",
			Content: bigGoTest,
			MustKeep: []string{
				"--- FAIL: TestOrderTotal_1",
				"total mismatch: want 4201 got 421",
				"--- FAIL: TestOrderTotal_2",
				"total mismatch: want 4202 got 422",
			},
			Expect: ExpectSqueeze,
		},
		{
			Name:    "pytest_traceback_400_passing",
			Class:   "should-squeeze",
			Model:   "claude-opus-4-5",
			Notes:   "pytest run whose only signal is the trailing traceback",
			Content: pytestOutput(400),
			MustKeep: []string{
				"AssertionError: refund amount mismatch: 420 != 4200",
				"FAILED tests/test_billing.py::test_refund_idempotency",
				"tests/test_billing.py:88: AssertionError",
			},
			Expect: ExpectSqueeze,
		},
		{
			Name:    "k8s_logs_1200_lines",
			Class:   "should-squeeze",
			Model:   "gpt-5",
			Notes:   "timestamped leveled logs, 2 critical lines among 1200",
			Content: k8sLogs(1200),
			MustKeep: []string{
				"pq: deadlock detected on relation ledger_entries",
				"exiting: context deadline exceeded after 3 retries",
			},
			Expect: ExpectSqueeze,
		},
		{
			Name:     "pnpm_ansi_progress_spam",
			Class:    "should-squeeze",
			Model:    "gpt-5",
			Notes:    "ANSI escapes + CR redraws; real error at the tail",
			Content:  ansiProgress(1500),
			MustKeep: []string{"ERR_PNPM_PEER_DEP_ISSUES"},
			Expect:   ExpectSqueeze,
		},
		{
			Name:     "deepseek_gate_small_log",
			Class:    "size-gate",
			Model:    "deepseek-chat",
			Notes:    "1.5KB log: above deepseek MinBytes=1024, below claude 4096",
			Content:  k8sLogs(18),
			MustKeep: []string{},
			Expect:   ExpectEither,
		},
		{
			Name:     "claude_gate_3kb_log",
			Class:    "size-gate",
			Model:    "claude-opus-4-5",
			Notes:    "3KB log under claude MinBytes=4096 — must pass through",
			Content:  k8sLogs(34),
			MustKeep: []string{},
			Expect:   ExpectUntouched,
		},
		{
			Name:     "claude_gate_6kb_log",
			Class:    "size-gate",
			Model:    "claude-opus-4-5",
			Notes:    "6KB log above claude MinBytes=4096",
			Content:  k8sLogs(70),
			MustKeep: []string{"pq: deadlock detected on relation ledger_entries"},
			Expect:   ExpectEither,
		},
		{
			Name:       "go_test_200_fails_vs_maxkept50",
			Class:      "algorithm-limit",
			Model:      "gpt-5",
			Notes:      "200 distinct FAILs against MaxKept=50 rescue budget",
			Content:    goTestOutput(2600, manyFails),
			MustKeep:   mustKeepForFails(200),
			Expect:     ExpectSqueeze,
			KnownLimit: "compress.Params.MaxKept=50 caps rescued middle error lines",
		},
		{
			Name:     "go_source_dense_in_test_words",
			Class:    "must-not-touch",
			Model:    "claude-opus-4-5",
			Notes:    "adversarial: source code dense in FAILED/PASSED/assert — router trap",
			Content:  goSourceWithTestWords(),
			MustKeep: []string{"func assertInvariant0(got, want int) error {", "const PASSED = \"ok\""},
			Expect:   ExpectUntouched,
		},
		{
			Name:     "realistic_test_helper_source",
			Class:    "must-not-touch",
			Model:    "claude-opus-4-5",
			Notes:    "non-adversarial: ordinary Go test-helper file an agent reads routinely",
			Content:  realisticTestHelper(),
			MustKeep: []string{"func AssertNoError(t *testing.T, err error) {", "var Fixture89 = OrderFixture{"},
			Expect:   ExpectUntouched,
		},
		{
			Name:     "incident_postmortem_prose",
			Class:    "must-not-touch",
			Model:    "claude-opus-4-5",
			Notes:    "human prose mentioning ERROR/FATAL/failing",
			Content:  incidentProse(),
			MustKeep: []string{"Action items: enforce migration ordering in CI"},
			Expect:   ExpectUntouched,
		},
		{
			Name: "json_api_list_800_rows",
			// Not "must-not-touch": lifting this into a table is allowed. The
			// class exists to say what is being graded, and here that is
			// whether structured data survives the transform intact.
			Class: "structured-data",
			Model: "gpt-5",
			Notes: "large valid JSON; tabular lifting is allowed, so no JSON format contract",
			// No Format contract, deliberately. FormatJSON and ExpectEither
			// contradicted each other on this case: lifting 800 rows into a
			// Markdown table is the transform tabular distillation exists for,
			// and its output cannot parse as JSON, so the pair could only be
			// satisfied by declining to compress the canonical win case. Dropping
			// the contract does not drop the check — TestJSONEnvelopeLoss in
			// verify_test.go asserts on this exact blob that no envelope field
			// (has_more, object, list) disappears in whichever shape comes back,
			// which is the property FormatJSON was reaching for.
			Content: jsonAPIResponse(800),
			// Graded as needles rather than left to the verify test alone: a
			// model shown 800 rows with has_more gone cannot tell that more
			// pages exist, so the envelope is a never-elide fact like any
			// error line.
			MustKeep: []string{"has_more", "next_cursor", "total_count"},
			Expect:   ExpectEither,
		},
		{
			Name:     "unified_diff_with_lockfile",
			Class:    "format-contract",
			Model:    "claude-opus-4-5",
			Notes:    "code hunk + 300-line lockfile hunk",
			Content:  unifiedDiff(300),
			MustKeep: []string{"return 0, ErrNegativeQty"},
			Expect:   ExpectEither,
			Format:   FormatDiff,
		},
		{
			Name:        "user_role_fenced_terminal",
			Class:       "user-path",
			Model:       "claude-opus-4-5",
			Notes:       "agent frameworks wrap tool output in a user message fence",
			Content:     bigGoTest,
			MustKeep:    []string{"--- FAIL: TestOrderTotal_1", "Explain the root cause first."},
			Expect:      ExpectSqueeze,
			Role:        "user",
			UserWrapper: "fence-terminal",
		},
		{
			Name:        "user_role_file_content_source",
			Class:       "user-path",
			Model:       "claude-opus-4-5",
			Notes:       "source file wrapped in <file_content> — must not be elided",
			Content:     goSourceWithTestWords(),
			MustKeep:    []string{"func assertInvariant0(got, want int) error {", "Do not skip the stack trace."},
			Expect:      ExpectUntouched,
			Role:        "user",
			UserWrapper: "file_content",
		},
	}
	return cases
}

func mustKeepForFails(n int) []string {
	out := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, fmt.Sprintf("--- FAIL: TestOrderTotal_%d", i))
	}
	return out
}

// MultiTurnSession builds a growing agent conversation: each turn appends a
// new tool result, and turn 3 re-reads the same file as turn 1. This is what
// prompt-cache stability and cross-turn dedup are measured against.
func MultiTurnSession(model string) [][]byte {
	fileRead := goSourceWithTestWords()
	turns := [][]byte{}

	msgs := []map[string]string{
		{"role": "user", "content": "the order total is wrong for negative quantities; find and fix it"},
	}
	add := func(role, content string) {
		msgs = append(msgs, map[string]string{"role": role, "content": content})
		cp := make([]map[string]string, len(msgs))
		copy(cp, msgs)
		body, err := json.Marshal(map[string]any{"model": model, "messages": cp})
		if err != nil {
			panic(err)
		}
		turns = append(turns, body)
	}

	add("tool", fileRead)                       // turn 1: read source
	add("tool", goTestOutput(1400, []int{611})) // turn 2: run tests
	add("tool", fileRead)                       // turn 3: re-read same file (dedup target)
	add("tool", k8sLogs(400))                   // turn 4: check logs
	return turns
}
