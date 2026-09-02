package compression

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// chatBody builds a chat-completions body with `turns` user messages of
// `size` bytes each, plus one system message.
func chatBody(turns, size int) []byte {
	msgs := []map[string]any{{"role": "system", "content": "you are a bot"}}
	for i := 0; i < turns; i++ {
		msgs = append(msgs, map[string]any{
			"role":    "user",
			"content": fmt.Sprintf("tool_result %d: %s", i, strings.Repeat("y", size)),
		})
	}
	body, err := json.Marshal(map[string]any{"model": "m", "messages": msgs})
	if err != nil {
		panic(err)
	}
	return body
}

// The fast path must be indistinguishable from running the full pipeline and
// finding nothing to do: same bytes, same zero result. Headroom below its
// reserve is the case that regressed — it used to parse and walk the whole body
// before reaching the same no-op.
func TestOptimizeRequestFastPathMatchesNoOpPass(t *testing.T) {
	body := chatBody(8, 8000) // ~64 KiB ⇒ ~16k estimated tokens, far below reserve
	if got := estimatedTokens(body); got >= DefaultHeadroomReserve {
		t.Fatalf("fixture too large: estimate %d >= reserve %d", got, DefaultHeadroomReserve)
	}

	out, res := OptimizeRequest(body, OptimizeOptions{Headroom: true})
	if res.Changed || res.HeadroomPruned || res.HeadroomSavedTokens != 0 {
		t.Errorf("below-reserve headroom reported work: %+v", res)
	}
	if string(out) != string(body) {
		t.Errorf("below-reserve headroom altered the body (%d → %d bytes)", len(body), len(out))
	}
}

// RTK's original tiny-body fast path must still hold now that the condition is
// shared with headroom.
func TestOptimizeRequestSkipsRTKBelowMinBytes(t *testing.T) {
	body := chatBody(1, 16) // well under MinCompressBytes
	if len(body) >= MinCompressBytes {
		t.Fatalf("fixture too large: %d >= %d", len(body), MinCompressBytes)
	}
	out, res := OptimizeRequest(body, OptimizeOptions{RTK: true, RTKParams: StandardRTKParams()})
	if res.Changed || res.RTKSavedBytes != 0 {
		t.Errorf("tiny body compressed: %+v", res)
	}
	if string(out) != string(body) {
		t.Error("tiny body altered")
	}
}

// Caveman has no size precondition: it injects a directive regardless of body
// size, so it must never be short-circuited — including when it rides along
// with a headroom profile that cannot fire.
func TestOptimizeRequestCavemanIsNeverSkipped(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts OptimizeOptions
	}{
		{"caveman alone on a tiny body", OptimizeOptions{Caveman: true, CavemanMode: ModeFull}},
		{"caveman with below-reserve headroom", OptimizeOptions{Caveman: true, CavemanMode: ModeFull, Headroom: true}},
		{"caveman with below-minbytes rtk", OptimizeOptions{Caveman: true, CavemanMode: ModeFull, RTK: true, RTKParams: StandardRTKParams()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := chatBody(1, 16)
			out, res := OptimizeRequest(body, tc.opts)
			if !res.CavemanInjected || !res.Changed {
				t.Fatalf("caveman did not inject: %+v", res)
			}
			if !strings.Contains(string(out), "caveman") {
				t.Error("directive missing from output")
			}
		})
	}
}

// Above the reserve headroom must still prune, so the new gate cannot be a
// blanket skip.
func TestOptimizeRequestHeadroomPrunesAboveReserve(t *testing.T) {
	body := chatBody(80, 7000) // ~560 KiB ⇒ ~140k estimated tokens
	if got := estimatedTokens(body); got < DefaultHeadroomReserve {
		t.Fatalf("fixture too small: estimate %d < reserve %d", got, DefaultHeadroomReserve)
	}
	out, res := OptimizeRequest(body, OptimizeOptions{Headroom: true})
	if !res.HeadroomPruned || !res.Changed {
		t.Fatalf("above-reserve headroom did not prune: %+v", res)
	}
	if len(out) >= len(body) {
		t.Errorf("pruned body not smaller: %d → %d", len(body), len(out))
	}
	// All system messages survive pruning.
	if !strings.Contains(string(out), "you are a bot") {
		t.Error("system message dropped by prune")
	}
}

// A reserve override low enough to trip on a small body proves the gate reads
// the resolved reserve rather than the default.
func TestOptimizeRequestHeadroomHonoursReserveOverride(t *testing.T) {
	body := chatBody(20, 500)
	est := estimatedTokens(body)
	if est >= DefaultHeadroomReserve {
		t.Fatalf("fixture unexpectedly large: %d", est)
	}
	_, res := OptimizeRequest(body, OptimizeOptions{Headroom: true, HeadroomReserve: est / 2, HeadroomKeep: 4})
	if !res.HeadroomPruned {
		t.Errorf("headroom ignored the reserve override (estimate %d, reserve %d): %+v", est, est/2, res)
	}
}
