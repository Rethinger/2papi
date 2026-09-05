// Command squozebench measures the squoze context optimizer against the
// contracts it publishes, offline and deterministically.
//
// It exercises the exact squoze version the gateway links (go.mod), not the
// upstream repo, because the claims under test are the gateway's.
//
// Metrics per case: byte savings, needle recall (never-elide), format safety
// (JSON/diff), idempotency, cross-engine determinism, latency p50/p95. Plus a
// multi-turn pass that measures prompt-cache prefix stability.
//
//	docker run --rm -v "$PWD:/src" -w /src golang:1.23 \
//	  go run ./test/squozebench
//
// Writes test/results/squoze_quality_report.json and per-case before/after
// artifacts under test/results/squoze_artifacts/ for token scoring.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Rethinger/squoze"
)

const (
	resultsDir   = "test/results"
	artifactsDir = "test/results/squoze_artifacts"
	latencyReps  = 12
)

// CaseReport is one corpus entry's full result.
type CaseReport struct {
	Name           string       `json:"name"`
	Class          string       `json:"class"`
	Model          string       `json:"model"`
	ModelFamily    string       `json:"model_family"`
	Notes          string       `json:"notes"`
	Role           string       `json:"role"`
	WireFormat     string       `json:"wire_format"`
	BlobBytesIn    int          `json:"blob_bytes_in"`
	BlobBytesOut   int          `json:"blob_bytes_out"`
	BodyBytesIn    int          `json:"body_bytes_in"`
	BodyBytesOut   int          `json:"body_bytes_out"`
	SavingsPct     float64      `json:"savings_pct"`
	BlocksSqueezed int          `json:"blocks_squeezed"`
	MemoHits       int          `json:"memo_hits"`
	Transforms     []string     `json:"transforms"`
	Needles        NeedleResult `json:"needles"`
	Format         FormatResult `json:"format"`
	Idempotent     bool         `json:"idempotent"`
	Deterministic  bool         `json:"deterministic"`
	Expectation    string       `json:"expectation"`
	Touched        bool         `json:"touched"`
	ExpectationMet bool         `json:"expectation_met"`
	KnownLimit     string       `json:"known_limit,omitempty"`
	LatencyP50MS   float64      `json:"latency_p50_ms"`
	LatencyP95MS   float64      `json:"latency_p95_ms"`
	Verdict        string       `json:"verdict"`
	Failures       []string     `json:"failures,omitempty"`
	ArtifactBefore string       `json:"artifact_before"`
	ArtifactAfter  string       `json:"artifact_after"`
}

// MultiTurnReport covers the prompt-cache stability pass.
type MultiTurnReport struct {
	Model        string             `json:"model"`
	Turns        int                `json:"turns"`
	BodyBytesIn  []int              `json:"body_bytes_in"`
	BodyBytesOut []int              `json:"body_bytes_out"`
	Squeezed     []int              `json:"blocks_squeezed_per_turn"`
	Transitions  []PrefixTransition `json:"transitions"`
	AnyBroken    bool               `json:"any_prefix_broken"`
	Verdict      string             `json:"verdict"`
}

// Report is the whole run.
type Report struct {
	Suite         string            `json:"suite"`
	SquozeVersion string            `json:"squoze_version"`
	Timestamp     string            `json:"timestamp"`
	Method        string            `json:"method"`
	Cases         []CaseReport      `json:"cases"`
	MultiTurn     []MultiTurnReport `json:"multi_turn"`
	Summary       map[string]any    `json:"summary"`
}

func main() {
	if err := os.MkdirAll(artifactsDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir:", err)
		os.Exit(1)
	}

	fmt.Println("================================================================")
	fmt.Printf(" squozebench — offline compression quality suite (squoze %s)\n", squoze.Version)
	fmt.Println(" No network, no API keys, deterministic corpus.")
	fmt.Println("================================================================")

	rep := Report{
		Suite:         "squoze offline compression quality",
		SquozeVersion: squoze.Version,
		Timestamp:     nowISO(),
		Method: "each case is built into an OpenAI-chat body, passed through a fresh " +
			"squoze.Engine, and graded against the tool's published contracts " +
			"(fail-open, never-elide, cache-safe, idempotent). Latency is the " +
			"median/p95 of 12 Apply calls on a warm engine.",
	}

	for _, c := range Corpus() {
		rep.Cases = append(rep.Cases, runCase(c))
	}

	for _, model := range []string{"claude-opus-4-5", "gpt-5"} {
		rep.MultiTurn = append(rep.MultiTurn, runMultiTurn(model))
	}

	rep.Summary = summarize(rep)
	writeReport(rep)
	printSummary(rep)
}

func runCase(c Case) CaseReport {
	body := c.bodyFor()
	inBlob := extractContent(body)

	// Fresh engine per case: no cross-case memo leakage.
	eng := squoze.NewEngine(squoze.DefaultMemoCapacity)

	out, res := eng.Apply(cloneBytes(body))
	outBlob := extractContent(out)

	r := CaseReport{
		Name:           c.Name,
		Class:          c.Class,
		Model:          c.Model,
		ModelFamily:    res.Family,
		Notes:          c.Notes,
		Role:           roleOf(c),
		WireFormat:     res.Format.String(),
		BlobBytesIn:    len(inBlob),
		BlobBytesOut:   len(outBlob),
		BodyBytesIn:    len(body),
		BodyBytesOut:   len(out),
		BlocksSqueezed: res.BlocksSqueezed,
		MemoHits:       res.MemoHits,
		Transforms:     res.Transforms,
		Expectation:    c.Expect.String(),
		KnownLimit:     c.KnownLimit,
	}
	if r.BlobBytesIn > 0 {
		r.SavingsPct = float64(r.BlobBytesIn-r.BlobBytesOut) / float64(r.BlobBytesIn) * 100
	}
	r.Touched = !bytes.Equal(body, out)

	r.Needles = checkNeedles(outBlob, c.MustKeep)
	r.Format = checkFormat(c.Format, inBlob, outBlob)

	// Idempotency: Apply(Apply(x)) must equal Apply(x) — otherwise the body
	// mutates every turn and the provider prompt cache never hits.
	out2, _ := eng.Apply(cloneBytes(out))
	r.Idempotent = bytes.Equal(out, out2)

	// Determinism: a second, independent engine must produce identical bytes.
	eng2 := squoze.NewEngine(squoze.DefaultMemoCapacity)
	out3, _ := eng2.Apply(cloneBytes(body))
	r.Deterministic = bytes.Equal(out, out3)

	// Latency on a warm engine.
	var lat []float64
	for i := 0; i < latencyReps; i++ {
		_, lr := eng.Apply(cloneBytes(body))
		lat = append(lat, lr.DurationMS)
	}
	r.LatencyP50MS = percentile(lat, 50)
	r.LatencyP95MS = percentile(lat, 95)

	// Expectation grading.
	switch c.Expect {
	case ExpectSqueeze:
		r.ExpectationMet = r.Touched && r.SavingsPct > 0
		if !r.ExpectationMet {
			r.Failures = append(r.Failures, "expected compression, body was left untouched")
		}
	case ExpectUntouched:
		r.ExpectationMet = !r.Touched
		if !r.ExpectationMet {
			r.Failures = append(r.Failures,
				fmt.Sprintf("expected passthrough, but %d block(s) were rewritten (%.1f%% of blob removed)",
					r.BlocksSqueezed, r.SavingsPct))
		}
	default:
		r.ExpectationMet = true
	}

	if r.Needles.Recall < 1 && c.KnownLimit == "" {
		r.Failures = append(r.Failures,
			fmt.Sprintf("never-elide violated: %d/%d facts lost", r.Needles.Total-r.Needles.Kept, r.Needles.Total))
	}
	if r.Format.Valid != nil && !*r.Format.Valid {
		r.Failures = append(r.Failures, "format contract violated: "+r.Format.Detail)
	}
	if !r.Idempotent {
		r.Failures = append(r.Failures, "not idempotent: Apply(Apply(x)) != Apply(x)")
	}
	if !r.Deterministic {
		r.Failures = append(r.Failures, "not deterministic across engines")
	}

	switch {
	case len(r.Failures) == 0 && c.KnownLimit != "" && r.Needles.Recall < 1:
		r.Verdict = "KNOWN-LIMIT"
	case len(r.Failures) == 0:
		r.Verdict = "PASS"
	default:
		r.Verdict = "FAIL"
	}

	// Artifacts for token scoring.
	base := filepath.Join(artifactsDir, c.Name)
	beforePath := base + ".before.txt"
	afterPath := base + ".after.txt"
	_ = os.WriteFile(beforePath, []byte(inBlob), 0o644)
	_ = os.WriteFile(afterPath, []byte(outBlob), 0o644)
	r.ArtifactBefore = beforePath
	r.ArtifactAfter = afterPath

	fmt.Printf("  %-38s %-16s %7.1f%% savings  recall %3.0f%%  %s  p50 %.2fms\n",
		trunc(c.Name, 38), c.Class, r.SavingsPct, r.Needles.Recall*100, pad(r.Verdict, 11), r.LatencyP50MS)
	for _, f := range r.Failures {
		fmt.Printf("      ! %s\n", f)
	}
	return r
}

func runMultiTurn(model string) MultiTurnReport {
	turns := MultiTurnSession(model)
	eng := squoze.NewEngine(squoze.DefaultMemoCapacity)

	rep := MultiTurnReport{Model: model, Turns: len(turns)}
	var processed [][]byte
	for _, t := range turns {
		out, res := eng.Apply(cloneBytes(t))
		rep.BodyBytesIn = append(rep.BodyBytesIn, len(t))
		rep.BodyBytesOut = append(rep.BodyBytesOut, len(out))
		rep.Squeezed = append(rep.Squeezed, res.BlocksSqueezed)
		processed = append(processed, out)
	}
	rep.Transitions = checkPrefixStability(processed)
	for _, tr := range rep.Transitions {
		if tr.PrefixBroken {
			rep.AnyBroken = true
		}
	}
	if rep.AnyBroken {
		rep.Verdict = "PREFIX-BROKEN"
	} else {
		rep.Verdict = "PREFIX-STABLE"
	}

	fmt.Printf("\n  multi-turn %s: %d turns, verdict %s\n", model, rep.Turns, rep.Verdict)
	for _, tr := range rep.Transitions {
		state := "stable"
		if tr.PrefixBroken {
			state = fmt.Sprintf("BROKEN at message %d", tr.FirstChangedIdx)
		}
		fmt.Printf("    turn %d→%d: %d/%d shared messages stable, %.1f%% of shared bytes (%s)\n",
			tr.FromTurn, tr.ToTurn, tr.StableMessages, tr.SharedMessages, tr.StablePrefixPct, state)
		if tr.ChangedDetail != "" {
			fmt.Printf("      → %s\n", tr.ChangedDetail)
		}
	}
	return rep
}

func summarize(rep Report) map[string]any {
	var pass, fail, known int
	var savings []float64
	var latP95 []float64
	violations := map[string][]string{}
	for _, c := range rep.Cases {
		switch c.Verdict {
		case "PASS":
			pass++
		case "KNOWN-LIMIT":
			known++
		default:
			fail++
			for _, f := range c.Failures {
				key := violationKey(f)
				violations[key] = append(violations[key], c.Name)
			}
		}
		if c.Touched {
			savings = append(savings, c.SavingsPct)
		}
		latP95 = append(latP95, c.LatencyP95MS)
	}
	sort.Float64s(savings)
	median := 0.0
	if len(savings) > 0 {
		median = savings[len(savings)/2]
	}
	brokenModels := []string{}
	for _, m := range rep.MultiTurn {
		if m.AnyBroken {
			brokenModels = append(brokenModels, m.Model)
		}
	}
	return map[string]any{
		"cases_total":                 len(rep.Cases),
		"cases_pass":                  pass,
		"cases_fail":                  fail,
		"cases_known_limit":           known,
		"cases_touched":               len(savings),
		"median_savings_pct_when_hit": round2(median),
		"max_latency_p95_ms":          round2(percentile(latP95, 100)),
		"contract_violations":         violations,
		"prefix_broken_models":        brokenModels,
	}
}

func printSummary(rep Report) {
	s := rep.Summary
	fmt.Println("\n================================================================")
	fmt.Printf(" cases: %v total · %v pass · %v fail · %v known-limit\n",
		s["cases_total"], s["cases_pass"], s["cases_fail"], s["cases_known_limit"])
	fmt.Printf(" compression fired on %v/%v cases · median savings when it fired: %v%%\n",
		s["cases_touched"], s["cases_total"], s["median_savings_pct_when_hit"])
	fmt.Printf(" worst-case p95 engine latency: %v ms\n", s["max_latency_p95_ms"])
	if v, ok := s["contract_violations"].(map[string][]string); ok && len(v) > 0 {
		fmt.Println(" contract violations:")
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("   - %s: %s\n", k, strings.Join(v[k], ", "))
		}
	}
	if bm, ok := s["prefix_broken_models"].([]string); ok && len(bm) > 0 {
		fmt.Printf(" prompt-cache prefix broken for: %s\n", strings.Join(bm, ", "))
	}
	fmt.Printf(" report: %s/squoze_quality_report.json\n", resultsDir)
	fmt.Println("================================================================")
}

func writeReport(rep Report) {
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "marshal:", err)
		os.Exit(1)
	}
	path := filepath.Join(resultsDir, "squoze_quality_report.json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "write:", err)
		os.Exit(1)
	}
}

// ---------- helpers ----------

func nowISO() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func cloneBytes(b []byte) []byte {
	c := make([]byte, len(b))
	copy(c, b)
	return c
}

func roleOf(c Case) string {
	if c.Role == "" {
		return "tool"
	}
	return c.Role
}

func violationKey(f string) string {
	switch {
	case strings.HasPrefix(f, "never-elide"):
		return "never-elide"
	case strings.HasPrefix(f, "expected passthrough"):
		return "touched-protected-content"
	case strings.HasPrefix(f, "expected compression"):
		return "no-compression"
	case strings.HasPrefix(f, "format contract"):
		return "format-safety"
	case strings.HasPrefix(f, "not idempotent"):
		return "idempotency"
	case strings.HasPrefix(f, "not deterministic"):
		return "determinism"
	default:
		return "other"
	}
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func pad(s string, n int) string {
	for len(s) < n {
		s += " "
	}
	return s
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}
