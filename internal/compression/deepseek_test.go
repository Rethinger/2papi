package compression

import (
	"encoding/json"
	"strings"
	"testing"
)

func deepseekBody(rc string) []byte {
	m := []map[string]any{
		{"role": "system", "content": "you are"},
		{"role": "user", "content": "hi"},
		{"role": "assistant", "content": "answer", "reasoning_content": rc},
	}
	b, _ := json.Marshal(map[string]any{"model": "deepseek-v4-pro", "messages": m})
	return b
}

func longReasoning() string {
	var sb strings.Builder
	for i := 0; i < 200; i++ {
		sb.WriteString("debug step thinking line number ")
		sb.WriteString(string(rune('0' + i%10)))
		sb.WriteByte('\n')
	}
	return sb.String()
}

func TestCompressReasoningHigh(t *testing.T) {
	body := deepseekBody(longReasoning())
	out, saved, ok := CompressReasoning(body, "high")
	if !ok || saved <= 0 {
		t.Fatalf("expected compress, ok=%v saved=%d", ok, saved)
	}
	var root map[string]json.RawMessage
	_ = json.Unmarshal(out, &root)
	var msgs []map[string]json.RawMessage
	_ = json.Unmarshal(root["messages"], &msgs)
	var rc string
	_ = json.Unmarshal(msgs[2]["reasoning_content"], &rc)
	if len(rc) >= len(longReasoning()) {
		t.Fatalf("reasoning not shortened: %d >= %d", len(rc), len(longReasoning()))
	}
	if !strings.Contains(rc, "elided") {
		t.Fatal("missing elided marker")
	}
}

func TestCompressReasoningLowTighter(t *testing.T) {
	body := deepseekBody(longReasoning())
	_, savedHigh, _ := CompressReasoning(body, "high")
	_, savedLow, _ := CompressReasoning(body, "low")
	// low keeps less → saves at least as much
	if savedLow < savedHigh {
		t.Fatalf("low should save >= high: low=%d high=%d", savedLow, savedHigh)
	}
}

func TestCompressReasoningSkipsSmall(t *testing.T) {
	body := deepseekBody("short reasoning")
	_, _, ok := CompressReasoning(body, "high")
	if ok {
		t.Fatal("should skip small reasoning")
	}
}

func TestEstimateTokens(t *testing.T) {
	if EstimateTokens([]byte(strings.Repeat("ab", 4))) != 2 {
		t.Fatalf("estimate: %d", EstimateTokens([]byte(strings.Repeat("ab", 4))))
	}
}
