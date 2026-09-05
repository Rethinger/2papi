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
// there". router.Classify returns KindTestOutput at testScore >= 3, counting
// substrings like "assert ", "FAILED", "PASSED", "=== RUN" across sampled
// windows. A test file that scores 2 today becomes elidable the moment someone
// adds one more assertion.
func TestClassificationMarginOnRealTestFiles(t *testing.T) {
	// Mirror of router.testHits (squoze v0.2.0, internal/router/router.go).
	testHits := []string{
		"--- FAIL", "--- PASS", "--- SKIP",
		"=== RUN", "=== CONT", "=== PAUSE",
		"go test", "testing:",
		"pytest", "PASSED", "FAILED",
		"vitest", "jest", "✓ ", "✗ ",
		"assert ", "AssertionError", "unittest",
	}

	type row struct {
		path     string
		size     int
		score    int
		elided   bool
		savedPct float64
	}
	var rows []row

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
