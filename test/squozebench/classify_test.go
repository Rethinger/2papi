package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Rethinger/squoze"
)

// TestClassificationMarginOnRealTestFiles measures how close real _test.go
// files sit to the router's test-output threshold.
//
// The elision hazard is not "does it fire today" but "how much margin is
// there". router.Classify returns KindTestOutput at testScore >= 3. A test file
// that scores 2 today becomes elidable the moment someone adds one more
// assertion.
//
// score below is a PARTIAL mirror of the v0.3.0 scorer, and partial on
// purpose:
//
//   - testHits, counted as substrings. Exact for these inputs: Classify counts
//     over up to three 32 KiB windows, and under 96 KiB that window set is the
//     whole string, which every file scanned here is.
//   - crashHits, counted once per line whose first token matches. New in
//     v0.3.0 - v0.2.0 scored testHits alone.
//   - diagnostic lines (router.countDiagnosticLines) are NOT mirrored:
//     reproducing looksLikeDiagnostic would fork engine internals into a
//     benchmark harness, and the router is behind internal/ so it cannot be
//     called.
//
// So score is a LOWER BOUND on the real testScore - a file printed at 2 may
// already be at 3 inside the router, never the other way round. The elided
// column carries no such caveat: it comes from the real pipeline through the
// public API.
func TestClassificationMarginOnRealTestFiles(t *testing.T) {
	// Mirrors of router.testHits and router.crashHits, squoze v0.3.0,
	// internal/router/router.go. testHits is byte-identical to v0.2.0.
	testHits := []string{
		"--- FAIL", "--- PASS", "--- SKIP",
		"=== RUN", "=== CONT", "=== PAUSE",
		"go test", "testing:",
		"pytest", "PASSED", "FAILED",
		"vitest", "jest", "✓ ", "✗ ",
		"assert ", "AssertionError", "unittest",
	}

	crashHits := []string{
		"panic:", "goroutine ", "exit status ", "signal: ",
		"FAIL", "ok  ", "PASS", "--- FAIL", "--- PASS", "--- SKIP",
		"Traceback (most recent call last):", "E   ", "OK (",
		"Caused by:", "at java.", "Error: ", "AssertionError",
	}

	type row struct {
		path     string
		size     int
		score    int
		elided   bool
		savedPct float64
	}
	var rows []row
	var skippedSelf bool

	roots := []string{"../../internal", "../../cmd", "../../control-plane/tests", "../../test"}
	for _, root := range roots {
		_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			base := filepath.Base(p)
			isTest := strings.HasSuffix(base, "_test.go") ||
				strings.HasSuffix(base, ".test.ts") ||
				strings.HasSuffix(base, ".test.mjs") ||
				strings.Contains(base, "_test.")
			if !isTest {
				return nil
			}
			// This file holds the marker lists as literals, so it scores high by
			// definition and says nothing about real test files. Skipped, and the
			// skip is logged rather than silent.
			if base == "classify_test.go" {
				skippedSelf = true
				return nil
			}
			if info.Size() < 4096 {
				return nil
			}
			raw, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			content := string(raw)

			score := 0
			for _, h := range testHits {
				score += strings.Count(content, h)
			}
			for _, line := range strings.Split(content, "\n") {
				if tl := strings.TrimSpace(line); tl != "" && hasAnyPrefix(tl, crashHits) {
					score++
				}
			}

			c := Case{Name: "t", Model: "claude-opus-4-5", Content: content}
			eng := squoze.NewEngine(squoze.DefaultMemoCapacity)
			out, _ := eng.Apply(c.bodyFor())
			got := extractContent(out)

			r := row{path: p, size: len(content), score: score, elided: got != content}
			if r.elided {
				r.savedPct = float64(len(content)-len(got)) / float64(len(content)) * 100
			}
			rows = append(rows, r)
			return nil
		})
	}

	if len(rows) == 0 {
		t.Skip("no test files >=4KB found")
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].score > rows[j].score })

	var atRisk, elided int
	var lines []string
	for _, r := range rows {
		if r.score >= 3 {
			atRisk++
		}
		if r.elided {
			elided++
		}
		state := "kept"
		if r.elided {
			state = fmt.Sprintf("ELIDED -%.0f%%", r.savedPct)
		}
		lines = append(lines, fmt.Sprintf("  score=%-4d size=%-7d %-12s %s",
			r.score, r.size, state, strings.TrimPrefix(r.path, "../../")))
	}

	t.Logf("real test files >=4KB scanned: %d", len(rows))
	if skippedSelf {
		t.Logf("  (this file excluded: its marker lists are literals)")
	}
	t.Logf("  score >= 3 (router would classify as test output): %d", atRisk)
	t.Logf("  actually elided by the full pipeline: %d", elided)
	t.Logf("top scorers:")
	for _, l := range lines[:min(12, len(lines))] {
		t.Log(l)
	}

	if atRisk > 0 && elided == 0 {
		t.Logf("NOTE: %d files score above the classifier threshold but survive because a later "+
			"guard (savings floor or line-count minimum) rejects the squeeze. The margin is thin: "+
			"the classifier already says 'test output' for these.", atRisk)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// hasAnyPrefix mirrors router.hasAnyPrefix: a line counts once, however many
// markers it starts with.
func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
