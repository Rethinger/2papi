package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Rethinger/2papi/internal/adapter"
	"github.com/Rethinger/2papi/internal/config"
)

func TestParseGenerateStream(t *testing.T) {
	raw := []byte(
		"data: {\"message_type\":\"system\",\"status\":\"in_progress\",\"content\":{\"content_type\":\"text\",\"parts\":[\"Generating\"]}}\n" +
			"data: {\"message_type\":\"assistant\",\"content\":{\"content_type\":\"multimodal_text\",\"parts\":[{\"content_type\":\"image_asset_pointer\",\"asset_pointer\":\"file-service://file-abc123\",\"metadata\":{}},{\"content_type\":\"text\",\"parts\":[\"A cat\"]}]},\"status\":\"finished_successfully\"}\n" +
			"data: {\"status\":\"finished_successfully\"}\n")
	pointers, revised, err := parseGenerateStream(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(pointers) != 1 || pointers[0] != "file-service://file-abc123" {
		t.Fatalf("pointers=%v", pointers)
	}
	if revised != "A cat" {
		t.Fatalf("revised=%q", revised)
	}
}

func TestParseGenerateStreamFailure(t *testing.T) {
	raw := []byte("data: {\"message_type\":\"assistant\",\"status\":\"failed\",\"content\":{\"content_type\":\"text\",\"parts\":[\"nope\"]}}\n")
	if _, _, err := parseGenerateStream(raw); err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("err=%v", err)
	}
}

func TestParseGenerateStreamIncomplete(t *testing.T) {
	raw := []byte("data: {\"message_type\":\"system\",\"status\":\"in_progress\"}\n")
	if _, _, err := parseGenerateStream(raw); err == nil {
		t.Fatal("expected error for unfinished stream")
	}
}

func TestImageDownloadURL(t *testing.T) {
	tests := []struct {
		base    string
		pointer string
		want    string
	}{
		{"https://chatgpt.com", "file-service://file-abc", "https://chatgpt.com/backend-api/files/file-abc/download"},
		{"https://chatgpt.com/", "file-service://file-abc", "https://chatgpt.com/backend-api/files/file-abc/download"},
		{"https://chatgpt.com", "https://cdn.example.test/img.png", "https://cdn.example.test/img.png"},
	}
	for _, tt := range tests {
		got, err := imageDownloadURL(tt.base, tt.pointer)
		if err != nil {
			t.Fatalf("pointer=%q err=%v", tt.pointer, err)
		}
		if got != tt.want {
			t.Fatalf("pointer=%q got=%q want=%q", tt.pointer, got, tt.want)
		}
	}
}

func TestExecuteImages(t *testing.T) {
	var generateHits, downloadHits int
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/generate":
			generateHits++
			if r.Header.Get("Authorization") != "Bearer tok" {
				t.Fatalf("missing authorization header")
			}
			if r.Header.Get("ChatGPT-Account-ID") != "acct-1" {
				t.Fatalf("missing ChatGPT-Account-ID header")
			}
			body, _ := io.ReadAll(r.Body)
			var payload generateRequest
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("bad generate payload: %v", err)
			}
			if payload.ForceParagenModelSlug != "gpt-image-1" || payload.Prompt != "a red cat" || !payload.HistoryAndTrainingDisabled {
				t.Fatalf("unexpected payload: %+v", payload)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"message_type\":\"assistant\",\"content\":{\"content_type\":\"multimodal_text\",\"parts\":[{\"content_type\":\"image_asset_pointer\",\"asset_pointer\":\"file-service://file-abc\",\"metadata\":{}},{\"content_type\":\"text\",\"parts\":[\"a red cat\"]}]},\"status\":\"finished_successfully\"}\n\n")
		case "/backend-api/files/file-abc/download":
			downloadHits++
			if r.Header.Get("Authorization") != "Bearer tok" {
				t.Fatalf("missing authorization header on download")
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("PNGDATA"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer backend.Close()

	ad := New(backend.Client(), nil, nil, Options{TestMode: true, BackendBaseURL: backend.URL})
	ex := adapter.Execution{
		Endpoint: adapter.EndpointImagesGenerations,
		Account: config.Account{
			Name:     "codex-1",
			Adapter:  Name,
			BaseURL:  backend.URL,
			Enabled:  true,
			Priority: 1,
			Credential: config.Credential{
				Kind:             "oauth",
				AccessToken:      "tok",
				ChatGPTAccountID: "acct-1",
			},
		},
		Model:       config.Model{Alias: "img", UpstreamModel: "gpt-image-1"},
		PublicModel: "img",
		Body:        []byte(`{"model":"img","prompt":"a red cat","n":1}`),
	}
	result, err := ad.Execute(context.Background(), ex)
	if err != nil {
		t.Fatal(err)
	}
	defer result.Body.Close()
	raw, _ := io.ReadAll(result.Body)
	if result.Status != http.StatusOK {
		t.Fatalf("status=%d body=%s", result.Status, raw)
	}
	var out struct {
		Created int64 `json:"created"`
		Data    []struct {
			B64JSON       string `json:"b64_json"`
			RevisedPrompt string `json:"revised_prompt"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data) != 1 {
		t.Fatalf("data=%+v", out.Data)
	}
	if out.Data[0].B64JSON != base64.StdEncoding.EncodeToString([]byte("PNGDATA")) {
		t.Fatalf("unexpected b64 payload %q", out.Data[0].B64JSON)
	}
	if out.Data[0].RevisedPrompt != "a red cat" {
		t.Fatalf("revised_prompt=%q", out.Data[0].RevisedPrompt)
	}
	if generateHits != 1 || downloadHits != 1 {
		t.Fatalf("generateHits=%d downloadHits=%d", generateHits, downloadHits)
	}
}

func TestExecuteImagesUpstreamError(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/backend-api/generate" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"detail":"unsupported model"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer backend.Close()

	ad := New(backend.Client(), nil, nil, Options{TestMode: true, BackendBaseURL: backend.URL})
	ex := adapter.Execution{
		Endpoint: adapter.EndpointImagesGenerations,
		Account: config.Account{Name: "codex-1", BaseURL: backend.URL, Enabled: true, Credential: config.Credential{Kind: "oauth", AccessToken: "tok", ChatGPTAccountID: "acct-1"}},
		Model:       config.Model{Alias: "img", UpstreamModel: "dall-e-3"},
		PublicModel: "img",
		Body:        []byte(`{"model":"img","prompt":"hi"}`),
	}
	_, err := ad.Execute(context.Background(), ex)
	if err == nil || !strings.Contains(err.Error(), "status 400") || !strings.Contains(err.Error(), "unsupported model") {
		t.Fatalf("err=%v", err)
	}
}
