package compression

import (
	"encoding/json"

	"github.com/Rethinger/2papi/internal/config"
)

// DefaultHeadroomReserve is the default max estimated input tokens before pruning (like 9Router ~120k).
const DefaultHeadroomReserve = 120000

// DefaultHeadroomKeep is how many recent messages to keep when pruning.
const DefaultHeadroomKeep = 8

// estimatedTokens uses a fast heuristic: ~4 chars per token (OpenAI approx).
func estimatedTokens(body []byte) int {
	return len(body) / 4
}

// PruneForHeadroom prunes old messages when estimated tokens exceed reserve.
// It keeps all system messages + last keep messages, dropping the middle.
// Returns (prunedBody, savedTokens, pruned bool).
func PruneForHeadroom(body []byte, reserve int, keep int) ([]byte, int, bool) {
	if reserve <= 0 {
		reserve = DefaultHeadroomReserve
	}
	if keep <= 0 {
		keep = DefaultHeadroomKeep
	}
	if estimatedTokens(body) < reserve {
		return body, 0, false
	}
	// Quick check: if no messages field, nothing to prune
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return body, 0, false
	}
	rawMsgs, ok := root["messages"]
	if !ok {
		return body, 0, false
	}
	var msgs []message
	if err := json.Unmarshal(rawMsgs, &msgs); err != nil {
		return body, 0, false
	}
	if len(msgs) <= keep+2 { // +2 for system margin
		return body, 0, false
	}
	// Collect system messages (keep all)
	var systemMsgs []message
	var otherMsgs []message
	for _, m := range msgs {
		if m.Role == "system" {
			systemMsgs = append(systemMsgs, m)
		} else {
			otherMsgs = append(otherMsgs, m)
		}
	}
	if len(otherMsgs) <= keep {
		return body, 0, false
	}
	// Keep last keep from otherMsgs
	keptOther := otherMsgs[len(otherMsgs)-keep:]
	pruned := append(systemMsgs, keptOther...)
	// If we pruned nothing (e.g., all system), skip
	if len(pruned) >= len(msgs) {
		return body, 0, false
	}
	reencoded, err := json.Marshal(pruned)
	if err != nil {
		return body, 0, false
	}
	root["messages"] = reencoded
	out, err := json.Marshal(root)
	if err != nil {
		return body, 0, false
	}
	saved := estimatedTokens(body) - estimatedTokens(out)
	if saved <= 0 {
		return body, 0, false
	}
	return out, saved, true
}

// ShouldRTK decides if RTK compression should run (legacy boolean API —
// new callers use DecideRTK, which also resolves the mode preset).
func ShouldRTK(global, model, vk *config.Optimization, header string) bool {
	return DecideRTK(global, model, vk, header).Run
}

// ShouldCaveman decides if caveman should run (legacy boolean API).
func ShouldCaveman(global, model, vk *config.Optimization, header string) bool {
	return DecideCaveman(global, model, vk, header).Run
}

// ShouldHeadroom decides if headroom should run based on global/model/vk/header
// (legacy API: concrete keep/reserve without the profile layer).
func ShouldHeadroom(global, model, vk *config.Optimization, header string) (bool, int, int) {
	run, _, reserve, keep := DecideHeadroom(global, model, vk, header)
	if !run {
		return false, 0, 0
	}
	return true, reserve, keep
}
