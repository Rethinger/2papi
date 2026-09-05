package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
)

// extractContent pulls the blob back out of a processed request body so it can
// be compared against the input. Path mirrors how the corpus builds bodies:
// messages.1.content.
func extractContent(body []byte) string {
	return gjson.GetBytes(body, "messages.1.content").String()
}

// NeedleResult reports the never-elide contract for one case.
type NeedleResult struct {
	Total  int      `json:"total"`
	Kept   int      `json:"kept"`
	Recall float64  `json:"recall"`
	Lost   []string `json:"lost,omitempty"`
}

// checkNeedles verifies that every must-keep fact survived verbatim.
func checkNeedles(out string, needles []string) NeedleResult {
	r := NeedleResult{Total: len(needles)}
	if len(needles) == 0 {
		r.Recall = 1
		return r
	}
	for _, n := range needles {
		if strings.Contains(out, n) {
			r.Kept++
		} else {
			r.Lost = append(r.Lost, n)
		}
	}
	r.Recall = float64(r.Kept) / float64(r.Total)
	return r
}

// FormatResult reports the structural-safety contract.
type FormatResult struct {
	Contract string `json:"contract"`
	Valid    *bool  `json:"valid"`
	Detail   string `json:"detail,omitempty"`
}

// checkFormat validates the structural invariant for the case, if any.
// A byte-identical passthrough always satisfies the contract.
func checkFormat(contract FormatContract, in, out string) FormatResult {
	switch contract {
	case FormatJSON:
		valid := json.Valid([]byte(out))
		detail := ""
		if !valid {
			detail = "output is not parseable JSON"
			if in == out {
				detail = "input itself was not valid JSON"
			}
		}
		return FormatResult{Contract: "json", Valid: &valid, Detail: detail}
	case FormatDiff:
		if in == out {
			t := true
			return FormatResult{Contract: "diff", Valid: &t, Detail: "unchanged"}
		}
		hasGit := strings.Contains(out, "diff --git")
		hasMinus := strings.Contains(out, "--- a/")
		hasPlus := strings.Contains(out, "+++ b/")
		hasHunk := strings.Contains(out, "@@")
		valid := hasGit && hasMinus && hasPlus && hasHunk
		detail := ""
		if !valid {
			var missing []string
			if !hasGit {
				missing = append(missing, "diff --git")
			}
			if !hasMinus {
				missing = append(missing, "--- a/")
			}
			if !hasPlus {
				missing = append(missing, "+++ b/")
			}
			if !hasHunk {
				missing = append(missing, "@@ hunk")
			}
			detail = "missing: " + strings.Join(missing, ", ")
		}
		return FormatResult{Contract: "diff", Valid: &valid, Detail: detail}
	default:
		return FormatResult{Contract: "none", Valid: nil}
	}
}

// commonPrefixLen returns the length of the shared byte prefix of a and b.
// This is the quantity provider prompt caches key on: a cache hit requires the
// prefix to be byte-stable, so any rewrite of an earlier turn truncates it.
func commonPrefixLen(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}

// PrefixTransition reports cache stability between two consecutive turns.
//
// Measured per message rather than on raw bytes: for every message index that
// exists in both turns, the processed content is compared. A provider prompt
// cache hits only on a byte-stable prefix, so the first index whose content
// changed is exactly where the cache stops hitting. Appending the new turn's
// messages is expected and is not a break.
type PrefixTransition struct {
	FromTurn        int     `json:"from_turn"`
	ToTurn          int     `json:"to_turn"`
	SharedMessages  int     `json:"shared_messages"`
	StableMessages  int     `json:"stable_messages"`
	FirstChangedIdx int     `json:"first_changed_message_index"`
	StableBytes     int     `json:"stable_prefix_bytes"`
	PrevBytes       int     `json:"prev_shared_bytes"`
	StablePrefixPct float64 `json:"stable_prefix_pct"`
	PrefixBroken    bool    `json:"prefix_broken"`
	ChangedDetail   string  `json:"changed_detail,omitempty"`
}

// messageContents extracts every message's role and content from a body.
func messageContents(body []byte) []struct{ Role, Content string } {
	var out []struct{ Role, Content string }
	gjson.GetBytes(body, "messages").ForEach(func(_, m gjson.Result) bool {
		out = append(out, struct{ Role, Content string }{
			Role:    m.Get("role").String(),
			Content: m.Get("content").String(),
		})
		return true
	})
	return out
}

// checkPrefixStability compares each processed turn against the previous one,
// message by message.
func checkPrefixStability(processed [][]byte) []PrefixTransition {
	var out []PrefixTransition
	for i := 1; i < len(processed); i++ {
		prev := messageContents(processed[i-1])
		cur := messageContents(processed[i])

		shared := len(prev)
		if len(cur) < shared {
			shared = len(cur)
		}

		t := PrefixTransition{
			FromTurn:        i,
			ToTurn:          i + 1,
			SharedMessages:  shared,
			FirstChangedIdx: -1,
		}

		for j := 0; j < shared; j++ {
			t.PrevBytes += len(prev[j].Content)
		}

		for j := 0; j < shared; j++ {
			if prev[j].Content == cur[j].Content {
				if t.FirstChangedIdx == -1 {
					t.StableMessages++
					t.StableBytes += len(prev[j].Content)
				}
				continue
			}
			if t.FirstChangedIdx == -1 {
				t.FirstChangedIdx = j
				t.PrefixBroken = true
				t.ChangedDetail = fmt.Sprintf(
					"message %d (role=%s) was rewritten between turns: %d bytes -> %d bytes",
					j, prev[j].Role, len(prev[j].Content), len(cur[j].Content))
			}
		}

		if t.PrevBytes > 0 {
			t.StablePrefixPct = float64(t.StableBytes) / float64(t.PrevBytes) * 100
		} else {
			t.StablePrefixPct = 100
		}
		out = append(out, t)
	}
	return out
}

// percentile returns the p-th percentile (0..100) of a sorted-in-place copy.
func percentile(vals []float64, p float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	cp := make([]float64, len(vals))
	copy(cp, vals)
	// insertion sort: N is tiny (repeat count)
	for i := 1; i < len(cp); i++ {
		for j := i; j > 0 && cp[j-1] > cp[j]; j-- {
			cp[j-1], cp[j] = cp[j], cp[j-1]
		}
	}
	if p <= 0 {
		return cp[0]
	}
	if p >= 100 {
		return cp[len(cp)-1]
	}
	idx := int(p / 100 * float64(len(cp)-1))
	return cp[idx]
}
