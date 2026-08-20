package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Rethinger/2papi/internal/adapter"
	"github.com/Rethinger/2papi/internal/config"
)

func TestTranslateOpenAIToGeminiPayload(t *testing.T) {
	openAIBody := []byte(`{
		"model": "gpt-4o",
		"messages": [
			{"role": "system", "content": "You are a code tutor."},
			{"role": "user", "content": "Write a python loop"},
			{"role": "assistant", "content": "for i in range(10): pass"},
			{"role": "user", "content": "Thanks!"}
		],
		"max_tokens": 2048,
		"temperature": 0.2,
		"stream": false
	}`)

	geminiBytes, stream, err := translateOpenAIToGemini(openAIBody)
	if err != nil {
		t.Fatal(err)
	}
	if stream {
		t.Fatal("expected stream=false")
	}

	var req geminiRequest
	if err := json.Unmarshal(geminiBytes, &req); err != nil {
		t.Fatal(err)
	}

	if req.SystemInstruction == nil || len(req.SystemInstruction.Parts) != 1 || req.SystemInstruction.Parts[0].Text != "You are a code tutor." {
		t.Fatalf("system instruction mismatch: %+v", req.SystemInstruction)
	}
	if len(req.Contents) != 3 {
		t.Fatalf("expected 3 non-system contents, got %d", len(req.Contents))
	}
	if req.Contents[0].Role != "user" || req.Contents[1].Role != "model" || req.Contents[2].Role != "user" {
		t.Fatalf("roles mismatch: %+v", req.Contents)
	}
	if req.GenerationConfig == nil || req.GenerationConfig.MaxOutputTokens != 2048 {
		t.Fatalf("generation config mismatch: %+v", req.GenerationConfig)
	}
}

func TestExecuteNonStreamingGeminiResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "generateContent") {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("key") != "secret-gemini-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"candidates": [
				{
					"content": {
						"parts": [{"text": "Hello from Gemini 2.0 Flash!"}],
						"role": "model"
					},
					"finishReason": "STOP"
				}
			],
			"usageMetadata": {
				"promptTokenCount": 12,
				"candidatesTokenCount": 8,
				"totalTokenCount": 20
			}
		}`)
	}))
	defer ts.Close()

	ad := New(ts.Client())
	res, err := ad.Execute(context.Background(), adapter.Execution{
		Endpoint:    adapter.EndpointChatCompletions,
		Account:     config.Account{BaseURL: ts.URL, APIKey: "secret-gemini-key"},
		Model:       config.Model{Alias: "gemini-public", UpstreamModel: "gemini-2.0-flash"},
		PublicModel: "gemini-public",
		Body:        []byte(`{"model":"gemini-public","messages":[{"role":"user","content":"hi"}]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	if res.Status != http.StatusOK {
		t.Fatalf("status=%d", res.Status)
	}

	b, _ := io.ReadAll(res.Body)
	var openAIResp struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(b, &openAIResp); err != nil {
		t.Fatalf("failed parsing converted response: %v, raw: %s", err, b)
	}

	if openAIResp.Model != "gemini-public" {
		t.Fatalf("model=%q", openAIResp.Model)
	}
	if len(openAIResp.Choices) != 1 || openAIResp.Choices[0].Message.Content != "Hello from Gemini 2.0 Flash!" {
		t.Fatalf("choices mismatch: %+v", openAIResp.Choices)
	}
	if openAIResp.Usage.TotalTokens != 20 {
		t.Fatalf("usage=%d", openAIResp.Usage.TotalTokens)
	}
}

func TestExecuteStreamingGeminiResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		events := []string{
			"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Gemini \"}]}}]}\n\n",
			"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Streaming!\"}]}}]}\n\n",
			"data: [DONE]\n\n",
		}
		for _, e := range events {
			_, _ = io.WriteString(w, e)
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	defer ts.Close()

	ad := New(ts.Client())
	res, err := ad.Execute(context.Background(), adapter.Execution{
		Endpoint:    adapter.EndpointChatCompletions,
		Account:     config.Account{BaseURL: ts.URL, APIKey: "key"},
		Model:       config.Model{Alias: "gemini-public", UpstreamModel: "gemini-2.0-flash"},
		PublicModel: "gemini-public",
		Body:        []byte(`{"model":"gemini-public","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	b, _ := io.ReadAll(res.Body)
	chunks := string(b)
	if !strings.Contains(chunks, "Gemini ") || !strings.Contains(chunks, "Streaming!") || !strings.Contains(chunks, "[DONE]") {
		t.Fatalf("unexpected streaming translation output:\n%s", chunks)
	}
}

func TestDiscoverAndValidateGeminiModels(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "models") {
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"models":[]}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	ad := New(ts.Client())
	res, err := ad.Operate(context.Background(), adapter.Operation{
		Kind:    adapter.OperationDiscoverModels,
		Account: config.Account{BaseURL: ts.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Models []struct {
			Slug string `json:"slug"`
		} `json:"models"`
	}
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Models) < 3 {
		t.Fatalf("expected at least 3 discovered gemini models, got %d", len(data.Models))
	}

	_, err = ad.Operate(context.Background(), adapter.Operation{
		Kind:    adapter.OperationValidateCredentials,
		Account: config.Account{BaseURL: ts.URL, APIKey: "valid-key"},
	})
	if err != nil {
		t.Fatalf("validation failed: %v", err)
	}
}
