package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Rethinger/squoze"
)

// contractPin gates the three tests that assert contracts the pinned squoze
// release violates. All three are fixed in squoze's working tree but not in any
// tag, so against the go.mod pin they can only fail — and a permanently red
// `go test ./...` is a signal nobody reads, which is worse than no signal. They
// therefore skip by default and run under SQUOZE_CONTRACT_PINS=1; CI runs them
// in a separate non-blocking job so the failures stay visible on GitHub.
//
// When go.mod pins a squoze above v0.2.0 and these pass with the variable set,
// delete the gate: from that point the pin protects a fixed contract instead of
// documenting a broken one. The test-to-bug table is in
// docs/benchmark-audit.md section 8.
func contractPin(t *testing.T, defect string) {
	t.Helper()
	if os.Getenv("SQUOZE_CONTRACT_PINS") == "" {
		t.Skipf("contract pin: %s is a known, unfixed defect of the pinned squoze release; "+
			"re-run with SQUOZE_CONTRACT_PINS=1 to see it fail", defect)
	}
}

// TestJSONTabularDeterminism checks whether two independent engines produce
// byte-identical output for the same JSON tool result. squoze's stated
// cache-safe contract requires it: "identical original bytes always produce
// byte-identical output".
func TestJSONTabularDeterminism(t *testing.T) {
	contractPin(t, "non-deterministic column order in tabular lifting")
	c := Case{Name: "json", Model: "gpt-5", Content: jsonAPIResponse(800)}
	body := c.bodyFor()

	seen := map[string]int{}
	var samples []string
	for i := 0; i < 12; i++ {
		eng := squoze.NewEngine(squoze.DefaultMemoCapacity)
		cp := make([]byte, len(body))
		copy(cp, body)
		out, _ := eng.Apply(cp)
		blob := extractContent(out)
		// Record just the header line, which carries the column order.
		header := blob
		if idx := strings.Index(blob, "\n|"); idx >= 0 {
			if end := strings.Index(blob[idx+1:], "\n"); end >= 0 {
				header = blob[idx+1 : idx+1+end]
			}
		}
		if seen[header] == 0 {
			samples = append(samples, header)
		}
		seen[header]++
	}
	if len(seen) > 1 {
		t.Errorf("NON-DETERMINISTIC: %d distinct column orders across 12 fresh engines", len(seen))
		for _, s := range samples {
			t.Logf("  variant (n=%d): %s", seen[s], s)
		}
	} else {
		t.Logf("deterministic across 12 engines: %s", samples[0])
	}
}

// TestJSONEnvelopeLoss documents what tabular lifting drops from a paginated
// API response.
//
// Assertion is on preservation, not on syntax. The original pin looked for the
// JSON-quoted key `"object"`, which can only appear if the output is still
// JSON — but lifting a list into a Markdown table is the intended transform, so
// that condition could be satisfied only by declining to compress. What the pin
// is actually for is that no envelope field silently disappears: a model shown
// 800 rows with has_more dropped cannot tell that more pages exist. Key and
// value both present, in whatever form, is the contract.
func TestJSONEnvelopeLoss(t *testing.T) {
	contractPin(t, "JSON envelope fields dropped by tabular lifting")
	c := Case{Name: "json", Model: "gpt-5", Content: jsonAPIResponse(800)}
	eng := squoze.NewEngine(squoze.DefaultMemoCapacity)
	out, _ := eng.Apply(c.bodyFor())
	blob := extractContent(out)

	for _, field := range []string{"has_more", "object", "list", "next_cursor", "total_count"} {
		if strings.Contains(blob, field) {
			t.Logf("kept: %s", field)
		} else {
			t.Errorf("ENVELOPE FIELD DROPPED: %s is absent from the distilled output", field)
		}
	}
	if json.Valid([]byte(blob)) {
		t.Logf("output is still valid JSON")
	} else {
		t.Logf("output is no longer JSON (it is a Markdown table) — %d bytes", len(blob))
	}
}

// TestDedupReExpandsHistory checks the cache-safety consequence of cross-turn
// dedup: re-reading the same file must not rewrite the EARLIER turn's content,
// because that invalidates the provider prompt-cache prefix.
//
// Mechanism of the failure this catches (squoze v0.2.0,
// internal/engine/stream_scanner.go): DeduplicateHistoricalReads replaces the
// earlier copy with a short "earlier view" marker. distillText() then returns
// "" for that marker (it is under the 64-byte floor), so the fallback restores
// out = t.content. But the very next guard is `out != "" && out != t.content`,
// which is now false — so the replacement is dropped and the message keeps its
// ORIGINAL bytes. Net effect: the earlier turn, which turn N-1 had already
// elided, is resent in full.
func TestDedupReExpandsHistory(t *testing.T) {
	contractPin(t, "cross-turn dedup re-expands the earlier turn")
	turns := MultiTurnSession("claude-opus-4-5")
	eng := squoze.NewEngine(squoze.DefaultMemoCapacity)

	var processed [][]byte
	for _, tn := range turns {
		cp := make([]byte, len(tn))
		copy(cp, tn)
		out, _ := eng.Apply(cp)
		processed = append(processed, out)
	}

	before := messageContents(processed[1]) // after turn 2
	after := messageContents(processed[2])  // after turn 3 (re-read)

	if len(before) < 2 || len(after) < 2 {
		t.Fatalf("unexpected message counts: %d / %d", len(before), len(after))
	}
	if before[1].Content == after[1].Content {
		t.Logf("history stable: message 1 unchanged across the re-read (%d bytes)", len(before[1].Content))
		return
	}
	delta := len(after[1].Content) - len(before[1].Content)
	dir := "shrank"
	if delta > 0 {
		dir = "GREW"
	}
	t.Errorf("PREFIX BROKEN: message 1 was rewritten on turn 3 — %d -> %d bytes (%s by %d)",
		len(before[1].Content), len(after[1].Content), dir, abs(delta))
	if delta > 0 {
		t.Errorf("  the earlier turn was RE-EXPANDED to its uncompressed form, so every " +
			"cached token from that point on is invalidated")
	}
}

// TestIdempotencyAcrossCorpus asserts Apply(Apply(x)) == Apply(x) for every
// case: a body that keeps mutating can never hit a provider prompt cache.
func TestIdempotencyAcrossCorpus(t *testing.T) {
	for _, c := range Corpus() {
		eng := squoze.NewEngine(squoze.DefaultMemoCapacity)
		body := c.bodyFor()
		once, _ := eng.Apply(append([]byte(nil), body...))
		twice, _ := eng.Apply(append([]byte(nil), once...))
		if !bytes.Equal(once, twice) {
			t.Errorf("%s: not idempotent (%d -> %d bytes on second pass)",
				c.Name, len(once), len(twice))
		}
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
