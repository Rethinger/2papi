package compression

import (
	"bytes"
	"encoding/json"
)

// Auto-mode heuristics: when a layer resolves to ModeAuto, these functions
// pick the concrete mode from request characteristics right before execution.
//
// Design constraints (from deep-gap research on provider prompt caches):
//   - RTK decisions are PER BLOCK (a tool_result's size is stable between
//     turns, so the same block always gets the same treatment and cached
//     prefixes survive);
//   - Headroom no-ops below half the reserve instead of pruning constantly;
//   - Caveman keys on agentic traffic shape (tools present).

// autoRTKLightMaxChars / autoRTKStandardMaxChars are block-size thresholds
// in characters (est tokens ≈ chars/4).
const (
	autoRTKLightMaxChars    = 4_000
	autoRTKStandardMaxChars = 32_000
)

// AutoRTKParamsForBlock selects compression parameters for ONE tool_result
// block by its size: small blocks are handled gently or skipped entirely,
// huge ones are compressed hard where the tokens are expensive.
func AutoRTKParamsForBlock(blockLen int) RTKParams {
	switch {
	case blockLen < autoRTKLightMaxChars:
		return RTKParamsFor(ModeLight)
	case blockLen < autoRTKStandardMaxChars:
		return RTKParamsFor(ModeStandard)
	default:
		return RTKParamsFor(ModeAggressive)
	}
}

// AutoHeadroomProfile maps estimated input tokens against the configured
// reserve: plenty of room → don't prune at all; approaching the limit →
// prune conservatively; nearly full → prune aggressively.
func AutoHeadroomProfile(estTokens, reserve int) (run bool, profile string) {
	if reserve <= 0 {
		reserve = DefaultHeadroomReserve
	}
	switch {
	case estTokens*2 < reserve: // ratio < 0.5
		return false, ""
	case estTokens*10 <= reserve*9: // ratio ≤ 0.9
		return true, ModeConservative
	default:
		return true, ModeAggressive
	}
}

var autoCavemanNeedles = [][]byte{
	[]byte(`"tools"`),
	[]byte(`"tool_calls"`),
	[]byte(`"role":"tool"`),
	[]byte(`"role": "tool"`),
	[]byte(`"type":"tool_use"`),
	[]byte(`"type": "tool_use"`),
	[]byte(`"type":"tool_result"`),
	[]byte(`"type": "tool_result"`),
}

// AutoCavemanDirective returns the directive for a request: agentic traffic
// (tools/tool calls/results anywhere in the body) earns the FULL terse
// directive — agents save the most and misread least — while plain chat gets
// the gentler lite variant that distills style, not voice.
func AutoCavemanDirective(body []byte) string {
	for _, needle := range autoCavemanNeedles {
		if bytes.Contains(body, needle) {
			return CavemanDirective // full
		}
	}
	return CavemanLiteDirective
}

// autoProbe is a minimal structural probe used when byte needles are not
// enough (kept for future refinement; current callers use byte needles).
type autoProbe struct {
	Tools    json.RawMessage `json:"tools"`
	Messages []struct {
		Role string `json:"role"`
	} `json:"messages"`
}

// HasToolTraffic parses only the top-level fields needed to detect agentic
// traffic. Invalid JSON counts as non-agentic (fail-open).
func HasToolTraffic(body []byte) bool {
	var probe autoProbe
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	if len(probe.Tools) > 0 && !bytes.Equal(bytes.TrimSpace(probe.Tools), []byte("null")) {
		return true
	}
	for _, m := range probe.Messages {
		if m.Role == "tool" {
			return true
		}
	}
	return false
}
