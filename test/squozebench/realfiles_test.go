package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rethinger/squoze"
)

// TestRealRepoFilesAreNotElided feeds actual source files from this repository
// through the engine as if an agent had read them with a file-read tool.
//
// This is the check that decides whether the misclassification found on the
// synthetic corpus is an artifact of that corpus or a live hazard: these are
// unmodified files a coding agent reads in this repo every day.
func TestRealRepoFilesAreNotElided(t *testing.T) {
	// Walk the repo for Go and JS sources big enough to cross the size gates.
	var files []string
	roots := []string{"../../internal", "../../cmd", "../../test"}
	for _, root := range roots {
		_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			ext := filepath.Ext(p)
			if ext != ".go" && ext != ".mjs" {
				return nil
			}
			if info.Size() < 4096 { // below the claude gate; cannot compress anyway
				return nil
			}
			files = append(files, p)
			return nil
		})
	}
	if len(files) == 0 {
		t.Skip("no candidate files found")
	}

	type hit struct {
		path      string
		inBytes   int
		outBytes  int
		savedPct  float64
		elidedNum string
	}
	var elided []hit
	var selfFixtures []hit
	checked := 0

	// This harness's own corpus generators embed machine-output text verbatim —
	// that is their job. They are not code an agent reads for meaning, so a hit
	// on them is reported but does not fail the check; otherwise this pin stays
	// permanently red and stops signalling anything about real source files.
	isOwnFixture := func(p string) bool {
		return strings.Contains(filepath.ToSlash(p), "test/squozebench/")
	}

	for _, p := range files {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		checked++
		content := string(raw)

		c := Case{Name: "real", Model: "claude-opus-4-5", Content: content}
		eng := squoze.NewEngine(squoze.DefaultMemoCapacity)
		out, _ := eng.Apply(c.bodyFor())
		got := extractContent(out)

		if got == content {
			continue
		}
		marker := ""
		if i := strings.Index(got, "[... squoze:"); i >= 0 {
			end := strings.Index(got[i:], "]")
			if end > 0 {
				marker = got[i : i+end+1]
			}
		}
		h := hit{
			path:      p,
			inBytes:   len(content),
			outBytes:  len(got),
			savedPct:  float64(len(content)-len(got)) / float64(len(content)) * 100,
			elidedNum: marker,
		}
		if isOwnFixture(p) {
			selfFixtures = append(selfFixtures, h)
			continue
		}
		elided = append(elided, h)
	}

	t.Logf("checked %d real repo source files >=4KB", checked)
	for _, h := range selfFixtures {
		t.Logf("(excluded, this harness's own fixture generator) %s: %d -> %d bytes (-%.1f%%)  %s",
			h.path, h.inBytes, h.outBytes, h.savedPct, h.elidedNum)
	}
	if len(elided) == 0 {
		t.Logf("no real source file was elided — the synthetic trap does not reproduce on this repo")
		return
	}

	t.Errorf("%d of %d real source files were elided as if they were machine output:", len(elided), checked)
	for _, h := range elided {
		t.Errorf("  %s: %d -> %d bytes (-%.1f%%)  %s", h.path, h.inBytes, h.outBytes, h.savedPct, h.elidedNum)
	}
}
