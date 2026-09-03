package protocol

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestParseAndRewriteChat(t *testing.T) {
	body := []byte(`{"model":"public","stream":true,"user":"u","metadata":{"gateway_session":"s"},"messages":[{"role":"user","content":"hi"}]}`)
	meta, err := ParseChat(body)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Model != "public" || !meta.Stream || meta.User != "u" || meta.Metadata["gateway_session"] != "s" {
		t.Fatalf("unexpected metadata: %+v", meta)
	}

	rewritten, err := RewriteModel(body, "upstream")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(rewritten, &got); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "upstream" || got["user"] != "u" || got["messages"] == nil {
		t.Fatalf("rewrite lost fields: %s", rewritten)
	}
}

func TestInvalidJSON(t *testing.T) {
	if _, err := ParseChat([]byte(`{"model":`)); err == nil {
		t.Fatal("ParseChat accepted invalid JSON")
	}
	if _, err := RewriteModel([]byte(`{"model":`), "upstream"); err == nil {
		t.Fatal("RewriteModel accepted invalid JSON")
	}
}

func TestRewriteModelPreservesLargeJSONNumberLexeme(t *testing.T) {
	body := []byte(`{"model":"public","seed":900719925474099312345,"temperature":1.2300,"messages":[]}`)
	rewritten, err := RewriteModel(body, "upstream")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"model":"upstream"`, `"seed":900719925474099312345`, `"temperature":1.2300`} {
		if !bytes.Contains(rewritten, []byte(want)) {
			t.Fatalf("rewrite did not preserve %s in %s", want, rewritten)
		}
	}
}

func TestRewriteModelAndThinking(t *testing.T) {
	body := []byte(`{"model":"public","messages":[{"role":"user","content":"hi"}],"max_tokens":500}`)
	rewritten, err := RewriteModelAndThinking(body, "claude-opus-5", 2048)
	if err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	if err := json.Unmarshal(rewritten, &got); err != nil {
		t.Fatal(err)
	}

	if got["model"] != "claude-opus-5" {
		t.Fatalf("expected model claude-opus-5, got %v", got["model"])
	}

	th, ok := got["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("expected thinking block, got %v", got["thinking"])
	}
	if th["type"] != "enabled" || th["budget_tokens"] != float64(2048) {
		t.Fatalf("unexpected thinking config: %+v", th)
	}

	// max_tokens must be >= 2048 + 1024 = 3072
	if maxT, ok := got["max_tokens"].(float64); !ok || maxT < 3072 {
		t.Fatalf("expected max_tokens >= 3072, got %v", got["max_tokens"])
	}
}
