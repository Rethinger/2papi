package anthropic

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

func TestTranslateOpenAIToAnthropicMessages(t *testing.T) {
	openAIBody := []byte(`{
		"model": "gpt-4o",
		"messages": [
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": "Hello!"},
			{"role": "assistant", "content": "I will run a tool", "tool_calls": [
				{"id": "call_1", "type": "function", "function": {"name": "get_weather", "arguments": "{\"city\":\"London\"}"}}
			]},
			{"role": "tool", "tool_call_id": "call_1", "content": "Rainy, 12C"}
		],
		"tools": [
			{"type": "function", "function": {"name": "get_weather", "description": "Get city weather", "parameters": {"type": "object", "properties": {"city": {"type": "string"}}}}}
		],
		"max_tokens": 1024,
		"temperature": 0.7,
		"stream": false
	}`)

	anthropicBytes, stream, err := translateOpenAIToAnthropic(openAIBody, "claude-3-5-sonnet-20241022")
	if err != nil {
		t.Fatal(err)
	}
	if stream {
		t.Fatal("expected stream=false")
	}

	var req anthropicMessagesRequest
	if err := json.Unmarshal(anthropicBytes, &req); err != nil {
		t.Fatal(err)
	}

	if req.Model != "claude-3-5-sonnet-20241022" {
		t.Fatalf("model=%q", req.Model)
	}
	if req.System != "You are a helpful assistant." {
		t.Fatalf("system=%q", req.System)
	}
	if req.MaxTokens != 1024 {
		t.Fatalf("max_tokens=%d", req.MaxTokens)
	}
	if len(req.Messages) != 3 {
		t.Fatalf("expected 3 converted messages, got %d", len(req.Messages))
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "get_weather" {
		t.Fatalf("tools mismatch: %+v", req.Tools)
	}
}

func TestExecuteNonStreamingAnthropicResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("x-api-key") != "secret-anthropic-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("anthropic-version") != AnthropicVersion {
			http.Error(w, "bad version", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id": "msg_01X",
			"type": "message",
			"role": "assistant",
			"content": [{"type": "text", "text": "Hello from Claude!"}],
			"stop_reason": "end_turn",
			"usage": {"input_tokens": 15, "output_tokens": 8}
		}`)
	}))
	defer ts.Close()

	ad := New(ts.Client())
	res, err := ad.Execute(context.Background(), adapter.Execution{
		Endpoint:    adapter.EndpointChatCompletions,
		Account:     config.Account{BaseURL: ts.URL, APIKey: "secret-anthropic-key"},
		Model:       config.Model{Alias: "claude-public", UpstreamModel: "claude-3-5-sonnet-20241022"},
		PublicModel: "claude-public",
		Body:        []byte(`{"model":"claude-public","messages":[{"role":"user","content":"hi"}]}`),
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

	if openAIResp.Model != "claude-public" {
		t.Fatalf("model=%q", openAIResp.Model)
	}
	if len(openAIResp.Choices) != 1 || openAIResp.Choices[0].Message.Content != "Hello from Claude!" {
		t.Fatalf("choices mismatch: %+v", openAIResp.Choices)
	}
	if openAIResp.Usage.TotalTokens != 23 {
		t.Fatalf("usage=%d", openAIResp.Usage.TotalTokens)
	}
}

func TestExecuteStreamingAnthropicResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		events := []string{
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"role\":\"assistant\"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello \"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"world!\"}}\n\n",
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
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
		Account:     config.Account{BaseURL: ts.URL, APIKey: "secret-key"},
		Model:       config.Model{Alias: "claude-public", UpstreamModel: "claude-3-5-sonnet-20241022"},
		PublicModel: "claude-public",
		Body:        []byte(`{"model":"claude-public","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	b, _ := io.ReadAll(res.Body)
	chunks := string(b)
	if !strings.Contains(chunks, "Hello ") || !strings.Contains(chunks, "world!") || !strings.Contains(chunks, "[DONE]") {
		t.Fatalf("unexpected streaming translation output:\n%s", chunks)
	}
}

func TestDiscoverAndValidateModels(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"data":[]}`)
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
		t.Fatalf("expected at least 3 discovered claude models, got %d", len(data.Models))
	}

	_, err = ad.Operate(context.Background(), adapter.Operation{
		Kind:    adapter.OperationValidateCredentials,
		Account: config.Account{BaseURL: ts.URL, APIKey: "key"},
	})
	if err != nil {
		t.Fatalf("validation failed: %v", err)
	}
}
