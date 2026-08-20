package protocol

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestAnthropicToOpenAIChatRoundTrip(t *testing.T) {
	body := []byte(`{
		"model": "claude-fast",
		"max_tokens": 1024,
		"system": "You are helpful.",
		"messages": [
			{"role": "user", "content": "hi"},
			{"role": "assistant", "content": [{"type": "text", "text": "hello"}, {"type": "tool_use", "id": "tool_1", "name": "get_weather", "input": {"city": "Paris"}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "tool_1", "content": "sunny"}]}
		],
		"tools": [{"name": "get_weather", "description": "weather", "input_schema": {"type": "object"}}]
	}`)
	req, err := ParseAnthropicMessages(body)
	if err != nil {
		t.Fatal(err)
	}
	openAI, err := AnthropicToOpenAIChat(req, "claude-fast")
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		MaxToken int    `json:"max_tokens"`
		Messages []struct {
			Role         string `json:"role"`
			Content      any    `json:"content"`
			ToolCallID   string `json:"tool_call_id"`
			ToolCalls    []any  `json:"tool_calls"`
		} `json:"messages"`
		Tools []struct {
			Type string `json:"type"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(openAI, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Model != "claude-fast" || payload.MaxToken != 1024 || payload.Stream {
		t.Fatalf("payload mismatch: %+v", payload)
	}
	if len(payload.Messages) != 4 { // system + user + assistant + tool
		t.Fatalf("messages=%d", len(payload.Messages))
	}
	if payload.Messages[0].Role != "system" || payload.Messages[0].Content != "You are helpful." {
		t.Fatalf("system message mismatch: %+v", payload.Messages[0])
	}
	if len(payload.Messages[2].ToolCalls) != 1 {
		t.Fatalf("assistant tool_calls missing: %+v", payload.Messages[2])
	}
	if payload.Messages[3].Role != "tool" || payload.Messages[3].ToolCallID != "tool_1" {
		t.Fatalf("tool result message mismatch: %+v", payload.Messages[3])
	}
	if len(payload.Tools) != 1 {
		t.Fatalf("tools=%d", len(payload.Tools))
	}
}

func TestOpenAIResponseToAnthropic(t *testing.T) {
	openAI := []byte(`{
		"id": "chatcmpl-1",
		"model": "up",
		"choices": [{"index": 0, "message": {"role": "assistant", "content": "answer", "tool_calls": [{"id": "tc1", "type": "function", "function": {"name": "fn", "arguments": "{\"a\":1}"}}]}, "finish_reason": "tool_calls"}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 5}
	}`)
	out, err := OpenAIResponseToAnthropic(openAI, "claude-fast")
	if err != nil {
		t.Fatal(err)
	}
	var resp struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Role       string `json:"role"`
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			ID    string          `json:"id"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		Usage struct {
			Input  int64 `json:"input_tokens"`
			Output int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Model != "claude-fast" || resp.StopReason != "tool_use" {
		t.Fatalf("model=%q stop=%q", resp.Model, resp.StopReason)
	}
	if len(resp.Content) != 2 {
		t.Fatalf("content=%d", len(resp.Content))
	}
	if resp.Content[0].Text != "answer" || resp.Content[1].Type != "tool_use" || resp.Content[1].Name != "fn" {
		t.Fatalf("content mismatch: %+v", resp.Content)
	}
	if resp.Usage.Input != 10 || resp.Usage.Output != 5 {
		t.Fatalf("usage=%+v", resp.Usage)
	}
}

func TestNewOpenAISSEToAnthropicReader(t *testing.T) {
	events := []string{
		"data: {\"id\":\"cmpl-1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"}}]}\n\n",
		"data: {\"id\":\"cmpl-1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" world\"}}]}\n\n",
		"data: {\"id\":\"cmpl-1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n",
		"data: [DONE]\n\n",
	}
	reader := NewOpenAISSEToAnthropicReader(io.NopCloser(strings.NewReader(strings.Join(events, ""))), "claude-fast")
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, want := range []string{"event: message_start", "event: content_block_start", "event: content_block_delta", `"text_delta"`, "Hello", "world", "event: message_delta", `"stop_reason":"end_turn"`, "event: message_stop"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
}
