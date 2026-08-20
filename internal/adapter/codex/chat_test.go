package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Rethinger/2papi/internal/adapter"
	"github.com/Rethinger/2papi/internal/config"
)

func TestChatExecuteUsesResponsesAndReturnsChatCompletion(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != responsesPath {
			t.Fatalf("path=%s", r.URL.Path)
		}
		var request struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
			Store  bool   `json:"store"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != "upstream-model" || !request.Stream || request.Store {
			t.Fatalf("unexpected upstream request: %#v", request)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_live\",\"model\":\"upstream-model\",\"created_at\":1720000002,\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"works\"}]}],\"usage\":{\"input_tokens\":4,\"output_tokens\":1,\"total_tokens\":5}}}\n\ndata: [DONE]\n\n")
	}))
	defer up.Close()

	ad := New(up.Client(), nil, nil, Options{TestMode: true, BackendBaseURL: up.URL})
	body := []byte(`{"model":"public-model","messages":[{"role":"user","content":"test"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	result, err := ad.Execute(context.Background(), adapter.Execution{Endpoint: adapter.EndpointChatCompletions, Request: req, Account: codexAccount(), Model: config.Model{Alias: "public-model", UpstreamModel: "upstream-model"}, PublicModel: "public-model", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Body.Close()
	got, _ := io.ReadAll(result.Body)
	if result.Status != http.StatusOK || result.Header.Get("Content-Type") != "application/json" || !strings.Contains(string(got), `"object":"chat.completion"`) || !strings.Contains(string(got), `"content":"works"`) || !strings.Contains(string(got), `"model":"public-model"`) {
		t.Fatalf("status=%d headers=%v body=%s", result.Status, result.Header, got)
	}
}

func TestChatExecutePreservesUpstreamError(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"quota reached","code":"rate_limit"}}`)
	}))
	defer up.Close()
	ad := New(up.Client(), nil, nil, Options{TestMode: true, BackendBaseURL: up.URL})
	body := []byte(`{"model":"public-model","messages":[{"role":"user","content":"test"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	result, err := ad.Execute(context.Background(), adapter.Execution{Endpoint: adapter.EndpointChatCompletions, Request: req, Account: codexAccount(), Model: config.Model{Alias: "public-model", UpstreamModel: "upstream-model"}, PublicModel: "public-model", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Body.Close()
	got, _ := io.ReadAll(result.Body)
	if result.Status != http.StatusTooManyRequests || !strings.Contains(string(got), `"code":"rate_limit"`) || strings.Contains(string(got), `"chat.completion"`) {
		t.Fatalf("status=%d body=%s", result.Status, got)
	}
}

func TestConvertChatRequestToResponses(t *testing.T) {
	in := []byte(`{
		"model":"public-model",
		"messages":[
			{"role":"system","content":"system rules"},
			{"role":"developer","content":"developer rules"},
			{"role":"user","content":"hello"},
			{"role":"assistant","content":"working","tool_calls":[{"id":"call_1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Paris\"}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"sunny"}
		],
		"tools":[{"type":"function","function":{"name":"weather","description":"Weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}],
		"tool_choice":{"type":"function","function":{"name":"weather"}},
		"max_completion_tokens":321,
		"reasoning_effort":"medium",
		"stop":["END"],
		"stream":true
	}`)

	got, err := convertChatRequest(in, "upstream-model")
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Model        string `json:"model"`
		Instructions string `json:"instructions"`
		Input        []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			CallID  string `json:"call_id"`
			Name    string `json:"name"`
			Output  string `json:"output"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
		Tools []struct {
			Type        string          `json:"type"`
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"tools"`
		ToolChoice      any `json:"tool_choice"`
		MaxOutputTokens int `json:"max_output_tokens"`
		Reasoning       struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
		Stream bool `json:"stream"`
		Store  bool `json:"store"`
	}
	if err := json.Unmarshal(got, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Model != "upstream-model" || payload.Instructions != "system rules\ndeveloper rules" || payload.MaxOutputTokens != 0 || payload.Reasoning.Effort != "medium" || !payload.Stream || payload.Store {
		t.Fatalf("unexpected request envelope: %s", got)
	}
	if bytes.Contains(got, []byte("max_output_tokens")) {
		t.Fatalf("Codex backend does not accept max_output_tokens: %s", got)
	}
	if len(payload.Input) != 4 || payload.Input[0].Role != "user" || payload.Input[0].Content[0].Type != "input_text" || payload.Input[1].Role != "assistant" || payload.Input[2].Type != "function_call" || payload.Input[2].CallID != "call_1" || payload.Input[2].Name != "weather" || payload.Input[3].Type != "function_call_output" || payload.Input[3].Output != "sunny" {
		t.Fatalf("unexpected converted input: %s", got)
	}
	if len(payload.Tools) != 1 || payload.Tools[0].Type != "function" || payload.Tools[0].Name != "weather" || !bytes.Contains(payload.Tools[0].Parameters, []byte(`"required":["city"]`)) {
		t.Fatalf("unexpected converted tools: %s", got)
	}
	choice, _ := json.Marshal(payload.ToolChoice)
	if string(choice) != `{"name":"weather","type":"function"}` {
		t.Fatalf("unexpected tool choice: %s", choice)
	}
}

func TestConvertChatRequestAcceptsStandardClientSamplingMetadata(t *testing.T) {
	in := []byte(`{
		"model":"public-model",
		"messages":[{"role":"user","name":"codex-client","content":"hello"}],
		"temperature":0.7,"top_p":0.9,"presence_penalty":0,"frequency_penalty":0,
		"user":"connection-check","seed":42,"n":1,"parallel_tool_calls":true,
		"max_tokens":64,"max_completion_tokens":96,
		"stream":true,"stream_options":{"include_usage":true}
	}`)
	got, err := convertChatRequest(in, "upstream-model")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte(`"model":"upstream-model"`)) || !bytes.Contains(got, []byte(`"text":"hello"`)) || bytes.Contains(got, []byte("temperature")) || bytes.Contains(got, []byte("max_output_tokens")) {
		t.Fatalf("unexpected converted request: %s", got)
	}
}

func TestConvertChatRequestRejectsUnrepresentableFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"structured output", `{"model":"m","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_schema","json_schema":{"name":"answer","schema":{"type":"object"}}}}`},
		{"legacy functions", `{"model":"m","messages":[{"role":"user","content":"hi"}],"functions":[{"name":"old"}]}`},
		{"image part without url", `{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":" "}}]}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := convertChatRequest([]byte(tt.body), "upstream")
			if err == nil || !strings.Contains(err.Error(), "codex_feature_unsupported") && !strings.Contains(err.Error(), "invalid chat request") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestConvertChatRequestAcceptsImageInput(t *testing.T) {
	in := []byte(`{
		"model":"public-model",
		"messages":[
			{"role":"user","content":[{"type":"text","text":"what is in this photo?"},{"type":"image_url","image_url":{"url":"https://example.test/a.png"}}]},
			{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}
		]
	}`)
	got, err := convertChatRequest(in, "upstream-model")
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Input []struct {
			Role    string `json:"role"`
			Content []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				ImageURL string `json:"image_url"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(got, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Input) != 2 || payload.Input[0].Role != "user" || payload.Input[1].Role != "user" {
		t.Fatalf("unexpected converted input: %s", got)
	}
	first := payload.Input[0].Content
	if len(first) != 2 || first[0].Type != "input_text" || first[0].Text != "what is in this photo?" || first[1].Type != "input_image" || first[1].ImageURL != "https://example.test/a.png" {
		t.Fatalf("unexpected converted content: %s", got)
	}
	second := payload.Input[1].Content
	if len(second) != 1 || second[0].Type != "input_image" || second[0].ImageURL != "data:image/png;base64,AAAA" {
		t.Fatalf("unexpected converted content: %s", got)
	}
}

func TestConvertResponsesFinalToChatCompletion(t *testing.T) {
	in := []byte(`{
		"id":"resp_123","model":"upstream-model","created_at":1720000000,"status":"completed",
		"output":[
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"},{"type":"output_text","text":" world"}]},
			{"type":"function_call","id":"fc_1","call_id":"call_1","name":"weather","arguments":"{\"city\":\"Paris\"}"}
		],
		"usage":{"input_tokens":11,"output_tokens":7,"total_tokens":18}
	}`)
	got, err := convertResponsesFinalToChat(in, "public-model")
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		Model   string `json:"model"`
		Choices []struct {
			Index        int    `json:"index"`
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Role      string `json:"role"`
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string                           `json:"id"`
					Type     string                           `json:"type"`
					Function struct{ Name, Arguments string } `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
			Total      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatal(err)
	}
	if out.ID != "chatcmpl-resp_123" || out.Object != "chat.completion" || out.Created != 1720000000 || out.Model != "public-model" || len(out.Choices) != 1 {
		t.Fatalf("unexpected response envelope: %s", got)
	}
	choice := out.Choices[0]
	if choice.FinishReason != "tool_calls" || choice.Message.Role != "assistant" || choice.Message.Content != "Hello world" || len(choice.Message.ToolCalls) != 1 || choice.Message.ToolCalls[0].ID != "call_1" || choice.Message.ToolCalls[0].Function.Name != "weather" || choice.Message.ToolCalls[0].Function.Arguments != `{"city":"Paris"}` {
		t.Fatalf("unexpected choice: %s", got)
	}
	if out.Usage.Prompt != 11 || out.Usage.Completion != 7 || out.Usage.Total != 18 {
		t.Fatalf("unexpected usage: %s", got)
	}
}

func TestConvertResponsesSSEToChatChunks(t *testing.T) {
	in := io.NopCloser(strings.NewReader(
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_9\",\"created_at\":1720000001}}\n\n" +
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hi\"}\n\n" +
			"data: {\"type\":\"response.output_item.added\",\"output_index\":1,\"item\":{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_7\",\"name\":\"weather\",\"arguments\":\"\"}}\n\n" +
			"data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":1,\"delta\":\"{\\\"city\\\":\"}\n\n" +
			"data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":1,\"delta\":\"\\\"Paris\\\"}\"}\n\n" +
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_9\",\"created_at\":1720000001,\"status\":\"completed\",\"output\":[{\"type\":\"function_call\"}],\"usage\":{\"input_tokens\":2,\"output_tokens\":3,\"total_tokens\":5}}}\n\n" +
			"data: [DONE]\n\n"))
	out := convertResponsesSSEToChat(in, "public-model")
	defer out.Close()
	b, err := io.ReadAll(out)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if strings.Count(text, `"role":"assistant"`) != 1 || !strings.Contains(text, `"content":"Hi"`) || !strings.Contains(text, `"index":0,"id":"call_7","type":"function"`) || !strings.Contains(text, `"arguments":"{\"city\":"`) || !strings.Contains(text, `"finish_reason":"tool_calls"`) || !strings.Contains(text, `"prompt_tokens":2`) || !strings.HasSuffix(strings.TrimSpace(text), "data: [DONE]") {
		t.Fatalf("unexpected stream:\n%s", text)
	}
}
