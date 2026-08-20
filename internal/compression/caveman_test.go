package compression

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInjectCavemanDirectiveAppendsToSystemMessage(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"system","content":"You are helpful."},{"role":"user","content":"hi"}]}`)
	out, ok, err := InjectCavemanDirective(body)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	var payload struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Messages[0].Role != "system" {
		t.Fatalf("first message role=%q", payload.Messages[0].Role)
	}
	if !strings.HasPrefix(payload.Messages[0].Content, "You are helpful.") || !strings.Contains(payload.Messages[0].Content, "smart caveman") {
		t.Fatalf("system prompt not extended: %q", payload.Messages[0].Content)
	}
	if len(payload.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(payload.Messages))
	}
}

func TestInjectCavemanDirectivePrependsSystemWhenMissing(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	out, ok, err := InjectCavemanDirective(body)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	var payload struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Messages) != 2 || payload.Messages[0].Role != "system" {
		t.Fatalf("expected prepended system message: %+v", payload.Messages)
	}
	if !strings.Contains(payload.Messages[0].Content, "smart caveman") {
		t.Fatalf("directive missing: %q", payload.Messages[0].Content)
	}
}

func TestInjectCavemanDirectiveExtendsInstructions(t *testing.T) {
	body := []byte(`{"model":"m","instructions":"Be brief.","input":"hi"}`)
	out, ok, err := InjectCavemanDirective(body)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	var payload struct {
		Instructions string `json:"instructions"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(payload.Instructions, "Be brief.") || !strings.Contains(payload.Instructions, "smart caveman") {
		t.Fatalf("instructions not extended: %q", payload.Instructions)
	}
}

func TestInjectCavemanDirectivePreservesUserContentAndOrder(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"system","content":"Sys."},{"role":"user","content":"again"}]}`)
	out, ok, err := InjectCavemanDirective(body)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	var payload struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Messages[0].Role != "user" || payload.Messages[1].Role != "system" || payload.Messages[2].Role != "user" {
		t.Fatalf("message order broken: %+v", payload.Messages)
	}
	if !strings.Contains(payload.Messages[1].Content, "smart caveman") {
		t.Fatalf("system content not extended: %q", payload.Messages[1].Content)
	}
	if payload.Messages[2].Content != "again" {
		t.Fatalf("user content changed: %q", payload.Messages[2].Content)
	}
}

func TestInjectCavemanDirectiveInvalidJSONSkipped(t *testing.T) {
	body := []byte(`{"model":"m","messages":[`)
	_, ok, err := InjectCavemanDirective(body)
	if err == nil || ok {
		t.Fatalf("expected error and no modification, ok=%v err=%v", ok, err)
	}
}
