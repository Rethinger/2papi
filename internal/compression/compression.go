package compression

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	MinCompressBytes = 2048
	HeadLines        = 20
	TailLines        = 20
)

type message struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  json.RawMessage `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

type chatPayload struct {
	Model    string          `json:"model"`
	Messages []message       `json:"messages"`
	Rest     map[string]json.RawMessage `json:"-"`
}

func CompressToolResults(body []byte) ([]byte, int, bool) {
	if len(body) < MinCompressBytes {
		return body, 0, false
	}

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

	modified := false
	savedTotal := 0

	for i, msg := range msgs {
		if msg.Role != "tool" && msg.Role != "user" {
			continue
		}

		var strContent string
		if err := json.Unmarshal(msg.Content, &strContent); err != nil {
			continue
		}

		if len(strContent) < MinCompressBytes {
			continue
		}

		compressed, saved := compressText(strContent)
		if saved > 0 {
			raw, err := json.Marshal(compressed)
			if err == nil {
				msgs[i].Content = raw
				modified = true
				savedTotal += saved
			}
		}
	}

	if !modified {
		return body, 0, false
	}

	reencodedMsgs, err := json.Marshal(msgs)
	if err != nil {
		return body, 0, false
	}

	root["messages"] = reencodedMsgs
	out, err := json.Marshal(root)
	if err != nil {
		return body, 0, false
	}

	return out, savedTotal, true
}

func compressText(input string) (string, int) {
	// Command-aware RTK: detect git/test/grep/ls and use tighter limits (like rtk-ai/rtk Rust)
	headLines, tailLines := HeadLines, TailLines
	lowerHead := strings.ToLower(input[:min(500, len(input))])
	switch {
	case strings.Contains(lowerHead, "diff --git") || strings.Contains(input, "@@ "):
		headLines, tailLines = 10, 10 // git diff
	case strings.Contains(lowerHead, "commit ") && strings.Contains(lowerHead, "author:"):
		headLines, tailLines = 8, 8 // git log
	case strings.Contains(lowerHead, "test") && (strings.Contains(input, "PASS") || strings.Contains(input, "FAIL")):
		// cargo test / npm test / go test — keep failures only
		return compressTestOutput(input)
	case strings.Contains(lowerHead, "grep") || strings.Contains(input, "rg "):
		headLines, tailLines = 15, 15
	}

	lines := strings.Split(input, "\n")
	if len(lines) <= headLines+tailLines+5 {
		return input, 0
	}

	head := lines[:headLines]
	tail := lines[len(lines)-tailLines:]
	elidedCount := len(lines) - (headLines + tailLines)

	var sb strings.Builder
	for _, l := range head {
		sb.WriteString(l)
		sb.WriteByte('\n')
	}
	sb.WriteString(fmt.Sprintf("[... %d lines elided by gateway compression ...]\n", elidedCount))
	for i, l := range tail {
		sb.WriteString(l)
		if i < len(tail)-1 {
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func compressTestOutput(input string) (string, int) {
	lines := strings.Split(input, "\n")
	var kept []string
	var failedCount, passedCount int
	for _, l := range lines {
		ll := strings.ToLower(l)
		if strings.Contains(ll, "fail") || strings.Contains(ll, "error") || strings.Contains(ll, "panic") {
			kept = append(kept, l)
			failedCount++
		} else if strings.Contains(ll, "pass") || strings.Contains(ll, "ok ") {
			passedCount++
		}
	}
	if failedCount == 0 {
		// No failures, collapse to counts
		collapsed := fmt.Sprintf("[... %d passed, %d failed — collapsed by gateway ...]\n", passedCount, failedCount)
		// keep first 5 and last 5 for context
		if len(lines) > 10 {
			kept = append(lines[:5], collapsed)
			kept = append(kept, lines[len(lines)-5:]...)
		} else {
			kept = []string{collapsed}
		}
	} else if len(kept) < 20 {
		// keep failures plus head/tail
		head := lines[:min(5, len(lines))]
		tail := lines[max(0, len(lines)-5):]
		kept = append(head, fmt.Sprintf("[... %d failures, %d passed kept ...]", failedCount, passedCount))
		kept = append(kept, tail...)
		// dedup
		seen := map[string]bool{}
		var dedup []string
		for _, k := range kept {
			if !seen[k] {
				seen[k] = true
				dedup = append(dedup, k)
			}
		}
		kept = dedup
	}
	compressed := strings.Join(kept, "\n")
	saved := len(input) - len(compressed)
	if saved <= 0 {
		return input, 0
	}
	return compressed, saved
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
