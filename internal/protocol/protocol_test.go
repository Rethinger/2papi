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
