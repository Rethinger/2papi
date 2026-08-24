package compression

import (
	"strings"
	"testing"
)

func TestAutoRTKParamsForBlock(t *testing.T) {
	small := AutoRTKParamsForBlock(2_000) // < 4k chars
	if small != RTKParamsFor(ModeLight) {
		t.Fatalf("small block must map to light: %+v", small)
	}
	mid := AutoRTKParamsForBlock(10_000)
	if mid != RTKParamsFor(ModeStandard) {
		t.Fatalf("mid block must map to standard: %+v", mid)
	}
	huge := AutoRTKParamsForBlock(50_000)
	if huge != RTKParamsFor(ModeAggressive) {
		t.Fatalf("huge block must map to aggressive: %+v", huge)
	}
	// Stability guarantee: the same block size always maps to the same preset.
	if AutoRTKParamsForBlock(10_000) != mid {
		t.Fatalf("per-block decision must be deterministic (prompt-cache safety)")
	}
}

func TestCompressToolResultsAutoPerBlock(t *testing.T) {
	mk := func(n int) string { return strings.Repeat("l", 95) + "\n" + strings.Repeat("line\n", n) }
	body := []byte(`{"model":"m","messages":[` +
		`{"role":"tool","content":` + jsonStringForTest(mk(50)) + `},` + // ~350 chars → light → skipped (<8192)
		`{"role":"tool","content":` + jsonStringForTest(mk(5000)) + `}` + // ~25k chars → standard → compressed
		`]}`)
	out, saved, ok := CompressToolResultsAuto(body)
	if !ok || saved == 0 {
		t.Fatalf("auto must compress the big block, saved=%d", saved)
	}
	s := string(out)
	if got := strings.Count(s, elisionMarker); got != 1 {
		t.Fatalf("exactly one block compressed, markers=%d", got)
	}
	// Second run is a no-op: per-block decisions are stable and elided blocks
	// are never re-compressed (provider prompt-cache safety).
	out2, saved2, ok2 := CompressToolResultsAuto(out)
	if !ok2 && saved2 == 0 && string(out2) == s {
		return
	}
	if strings.Count(string(out2), elisionMarker) != 1 {
		t.Fatalf("second pass mutated history: markers=%d", strings.Count(string(out2), elisionMarker))
	}
}

func jsonStringForTest(s string) string {
	b := make([]byte, 0, len(s)+2)
	b = append(b, '"')
	for _, c := range []byte(s) {
		switch c {
		case '"':
			b = append(b, '\\', '"')
		case '\n':
			b = append(b, '\\', 'n')
		case '\\':
			b = append(b, '\\', '\\')
		default:
			b = append(b, c)
		}
	}
	return string(append(b, '"'))
}

func TestAutoHeadroomProfileBoundaries(t *testing.T) {
	const reserve = 100_000
	if run, _ := AutoHeadroomProfile(49_000, reserve); run {
		t.Fatalf("ratio <0.5 must not prune")
	}
	if run, profile := AutoHeadroomProfile(70_000, reserve); !run || profile != ModeConservative {
		t.Fatalf("mid ratio → conservative, got run=%v %s", run, profile)
	}
	if run, profile := AutoHeadroomProfile(95_000, reserve); !run || profile != ModeAggressive {
		t.Fatalf("ratio >0.9 → aggressive, got run=%v %s", run, profile)
	}
}

func TestAutoCavemanDirective(t *testing.T) {
	agentic := []byte(`{"model":"m","tools":[{"name":"bash"}],"messages":[{"role":"user","content":"hi"}]}`)
	if AutoCavemanDirective(agentic) != CavemanDirective {
		t.Fatalf("tools present → full directive")
	}
	plain := []byte(`{"model":"m","messages":[{"role":"user","content":"привет, расскажи сказку"}]}`)
	if AutoCavemanDirective(plain) != CavemanLiteDirective {
		t.Fatalf("plain chat → lite directive")
	}
	openaiTools := []byte(`{"model":"m","messages":[{"role":"assistant","tool_calls":[{"id":"t"}]}]}`)
	if AutoCavemanDirective(openaiTools) != CavemanDirective {
		t.Fatalf("tool_calls → full directive")
	}
}

func TestHasToolTraffic(t *testing.T) {
	if !HasToolTraffic([]byte(`{"tools":[],"messages":[{"role":"tool"}]}`)) {
		t.Fatalf("tool role must be detected")
	}
	if HasToolTraffic([]byte(`{"messages":[{"role":"user"}]}`)) {
		t.Fatalf("plain chat has no tool traffic")
	}
	if HasToolTraffic([]byte(`not json`)) {
		t.Fatalf("invalid json fails open as non-agentic")
	}
}
