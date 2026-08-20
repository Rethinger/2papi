package compression

import (
	"encoding/json"
	"strings"
)

// DeepSeek token estimates: DeepSeek ≈ 1 EN char ≈ 0.3 token, 1 zh char ≈ 0.6
// token (official "Token & Token Usage" doc). estimateDeepSeekTokens is a fast
// heuristic for headroom decisions without a tokenizer.
func estimateDeepSeekTokens(body []byte) int {
	// count CJK bytes loosely: treat multi-byte UTF-8 as zh-ish
	cjk := 0
	ascii := 0
	for i := 0; i < len(body); {
		c := body[i]
		if c < 0x80 {
			ascii++
			i++
		} else if c >= 0xC0 && c < 0xFE {
			// multi-byte: count as one CJK-ish char
			cjk++
			i++
			for i < len(body) && body[i]&0xC0 == 0x80 {
				i++
			}
		} else {
			i++
		}
	}
	return ascii/3 + cjk*3/5
}

// EstimateTokens is a public rough estimate (1 token ≈ 4 EN chars) for
// X-Gateway-Saved-Tokens style headers across providers.
func EstimateTokens(body []byte) int {
	if len(body) == 0 {
		return 0
	}
	return len(body) / 4
}

// CompressReasoning compresses large reasoning_content (CoT) in a DeepSeek chat
// payload: keeps head+tail lines, preserving the final answer fully. Mimics
// RTK but for reasoning, per reasoning_effort (low → tight, high/max → looser).
// Returns (newBody, savedBytes, modified).
func CompressReasoning(body []byte, effort string) ([]byte, int, bool) {
	head, tail := 20, 20
	if effort == "low" {
		head, tail = 8, 8
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return body, 0, false
	}
	rawMsg, ok := root["messages"]
	if !ok {
		return body, 0, false
	}
	var msgs []map[string]json.RawMessage
	if err := json.Unmarshal(rawMsg, &msgs); err != nil {
		return body, 0, false
	}
	modified := false
	var saved int
	for i, m := range msgs {
		role, _ := jsonString(m["role"])
		if role != "assistant" {
			continue
		}
		rawRC, ok := m["reasoning_content"]
		if !ok {
			continue
		}
		var text string
		if err := json.Unmarshal(rawRC, &text); err != nil || len(text) < 2048 {
			continue
		}
		compressed, s := compressReasoningText(text, head, tail)
		if s > 0 {
			enc, _ := json.Marshal(compressed)
			msgs[i]["reasoning_content"] = enc
			modified = true
			saved += s
		}
	}
	if !modified {
		return body, 0, false
	}
	reEnc, _ := json.Marshal(msgs)
	root["messages"] = reEnc
	out, _ := json.Marshal(root)
	return out, saved, true
}

func compressReasoningText(input string, headN, tailN int) (string, int) {
	lines := strings.Split(input, "\n")
	if len(lines) <= headN+tailN+5 {
		return input, 0
	}
	var sb strings.Builder
	for _, l := range lines[:headN] {
		sb.WriteString(l)
		sb.WriteByte('\n')
	}
	sb.WriteString("[... CoT elided by gateway ...]\n")
	for i, l := range lines[len(lines)-tailN:] {
		sb.WriteString(l)
		if i < tailN-1 {
			sb.WriteByte('\n')
		}
	}
	compressed := sb.String()
	saved := len(input) - len(compressed)
	if saved <= 0 {
		return input, 0
	}
	return compressed, saved
}

func jsonString(raw json.RawMessage) (string, bool) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}
