package protocol

import (
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
