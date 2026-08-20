package compression

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCompressToolResultsLargeOutput(t *testing.T) {
	var lines []string
	for i := 0; i < 100; i++ {
		lines = append(lines, strings.Repeat("a", 50))
	}
	largeContent := strings.Join(lines, "\n")

	payload := map[string]any{
		"model": "gpt-4o",
		"messages": []map[string]any{
			{"role": "user", "content": "run command"},
			{"role": "tool", "content": largeContent, "tool_call_id": "call_1"},
		},
	}
	body, _ := json.Marshal(payload)

	compressed, saved, wasCompressed := CompressToolResults(body)
	if !wasCompressed || saved <= 0 {
		t.Fatalf("expected compression: wasCompressed=%v saved=%d", wasCompressed, saved)
	}

	var out struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(compressed, &out); err != nil {
		t.Fatal(err)
	}

	if len(out.Messages) != 2 {
		t.Fatalf("messages count=%d", len(out.Messages))
	}
	toolContent := out.Messages[1].Content
	if !strings.Contains(toolContent, "lines elided by gateway compression") {
		t.Fatalf("elided marker missing: %s", toolContent)
	}
	if len(toolContent) >= len(largeContent) {
		t.Fatalf("compressed content should be smaller: %d >= %d", len(toolContent), len(largeContent))
	}
}

func TestCompressToolResultsSmallPayloadUnchanged(t *testing.T) {
	small := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"tool","content":"short output"}]}`)
	out, saved, wasCompressed := CompressToolResults(small)
	if wasCompressed || saved != 0 || string(out) != string(small) {
		t.Fatalf("small payload should remain untouched")
	}
}
