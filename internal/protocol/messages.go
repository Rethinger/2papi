package protocol

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// Anthropic-native /v1/messages support: the gateway translates inbound
// Anthropic Messages payloads to OpenAI chat completions, routes them through
// any provider adapter, and translates the response back to the Anthropic
// wire format (JSON or SSE).

type AnthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type AnthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

type AnthropicMessagesRequest struct {
	Model       string             `json:"model"`
	Messages    []AnthropicMessage `json:"messages"`
	System      json.RawMessage    `json:"system,omitempty"`
	MaxTokens   int                `json:"max_tokens,omitempty"`
	Temperature *float64           `json:"temperature,omitempty"`
	TopP        *float64           `json:"top_p,omitempty"`
	Stream      bool               `json:"stream,omitempty"`
	Tools       []AnthropicTool    `json:"tools,omitempty"`
}

func ParseAnthropicMessages(b []byte) (*AnthropicMessagesRequest, error) {
	var req AnthropicMessagesRequest
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&req); err != nil {
		return nil, err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("trailing json tokens")
	}
	if req.Model == "" {
		return nil, errors.New("model required")
	}
	return &req, nil
}

// AnthropicToOpenAIChat converts an Anthropic Messages payload into an OpenAI
// chat completion payload. The model field is set to publicModel so the
// gateway's alias routing applies.
func AnthropicToOpenAIChat(req *AnthropicMessagesRequest, publicModel string) ([]byte, error) {
	type openAIToolCall struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	type openAIMessage struct {
		Role         string           `json:"role"`
		Content      any              `json:"content"`
		ToolCalls    []openAIToolCall `json:"tool_calls,omitempty"`
		ToolCallID   string           `json:"tool_call_id,omitempty"`
	}
	type openAITool struct {
		Type     string `json:"type"`
		Function struct {
			Name        string          `json:"name"`
			Description string          `json:"description,omitempty"`
			Parameters  json.RawMessage `json:"parameters,omitempty"`
		} `json:"function"`
	}

	var systemText string
	if len(req.System) > 0 && !bytes.Equal(req.System, []byte("null")) {
		var s string
		if json.Unmarshal(req.System, &s) == nil {
			systemText = s
		} else {
			var parts []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(req.System, &parts) == nil {
				var sb strings.Builder
				for _, p := range parts {
					if p.Type == "text" && p.Text != "" {
						if sb.Len() > 0 {
							sb.WriteString("\n\n")
						}
						sb.WriteString(p.Text)
					}
				}
				systemText = sb.String()
			}
		}
	}

	messages := []openAIMessage{}
	if systemText != "" {
		messages = append(messages, openAIMessage{Role: "system", Content: systemText})
	}

	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			var text string
			if json.Unmarshal(m.Content, &text) == nil {
				messages = append(messages, openAIMessage{Role: "user", Content: text})
				continue
			}
			var blocks []struct {
				Type        string          `json:"type"`
				Text        string          `json:"text"`
				ToolUseID   string          `json:"tool_use_id"`
				Content     json.RawMessage `json:"content"`
				IsError     bool            `json:"is_error"`
			}
			if json.Unmarshal(m.Content, &blocks) != nil {
				messages = append(messages, openAIMessage{Role: "user", Content: string(m.Content)})
				continue
			}
			var textParts []string
			for _, b := range blocks {
				switch b.Type {
				case "text":
					if b.Text != "" {
						textParts = append(textParts, b.Text)
					}
				case "tool_result":
					var resultText string
					if json.Unmarshal(b.Content, &resultText) != nil {
						resultText = string(b.Content)
					}
					messages = append(messages, openAIMessage{Role: "tool", ToolCallID: b.ToolUseID, Content: resultText})
				}
			}
			if len(textParts) > 0 {
				messages = append(messages, openAIMessage{Role: "user", Content: strings.Join(textParts, "\n")})
			}
		case "assistant":
			var text string
			if json.Unmarshal(m.Content, &text) == nil {
				messages = append(messages, openAIMessage{Role: "assistant", Content: text})
				continue
			}
			var blocks []struct {
				Type  string          `json:"type"`
				Text  string          `json:"text"`
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			}
			if json.Unmarshal(m.Content, &blocks) != nil {
				messages = append(messages, openAIMessage{Role: "assistant", Content: string(m.Content)})
				continue
			}
			var textParts []string
			var toolCalls []openAIToolCall
			for _, b := range blocks {
				switch b.Type {
				case "text":
					if b.Text != "" {
						textParts = append(textParts, b.Text)
					}
				case "tool_use":
					tc := openAIToolCall{ID: b.ID, Type: "function"}
					tc.Function.Name = b.Name
					args := string(b.Input)
					if args == "" {
						args = "{}"
					}
					tc.Function.Arguments = args
					toolCalls = append(toolCalls, tc)
				}
			}
			msg := openAIMessage{Role: "assistant"}
			if len(textParts) > 0 {
				msg.Content = strings.Join(textParts, "\n")
			} else {
				msg.Content = ""
			}
			if len(toolCalls) > 0 {
				msg.ToolCalls = toolCalls
			}
			messages = append(messages, msg)
		}
	}

	tools := []openAITool{}
	for _, t := range req.Tools {
		if t.Name == "" {
			continue
		}
		tool := openAITool{Type: "function"}
		tool.Function.Name = t.Name
		tool.Function.Description = t.Description
		if len(t.InputSchema) > 0 {
			tool.Function.Parameters = t.InputSchema
		}
		tools = append(tools, tool)
	}

	payload := map[string]any{
		"model":    publicModel,
		"messages": messages,
		"stream":   req.Stream,
	}
	if req.MaxTokens > 0 {
		payload["max_tokens"] = req.MaxTokens
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		payload["top_p"] = *req.TopP
	}
	if len(tools) > 0 {
		payload["tools"] = tools
	}
	return json.Marshal(payload)
}

// OpenAIResponseToAnthropic converts a non-streaming OpenAI chat completion
// JSON document into the Anthropic Messages response format.
func OpenAIResponseToAnthropic(body []byte, requestedModel string) ([]byte, error) {
	var resp struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}

	content := []map[string]any{}
	stopReason := "end_turn"
	if len(resp.Choices) > 0 {
		choice := resp.Choices[0]
		if choice.Message.Content != "" {
			content = append(content, map[string]any{"type": "text", "text": choice.Message.Content})
		}
		for _, tc := range choice.Message.ToolCalls {
			var input any
			_ = json.Unmarshal([]byte(tc.Function.Arguments), &input)
			if input == nil {
				input = map[string]any{}
			}
			content = append(content, map[string]any{
				"type":  "tool_use",
				"id":    tc.ID,
				"name":  tc.Function.Name,
				"input": input,
			})
		}
		switch choice.FinishReason {
		case "tool_calls":
			stopReason = "tool_use"
		case "length":
			stopReason = "max_tokens"
		case "stop", "":
			stopReason = "end_turn"
		default:
			stopReason = choice.FinishReason
		}
	}

	out := map[string]any{
		"id":         resp.ID,
		"type":       "message",
		"role":       "assistant",
		"model":      requestedModel,
		"content":    content,
		"stop_reason": stopReason,
		"usage": map[string]any{
			"input_tokens":  resp.Usage.PromptTokens,
			"output_tokens": resp.Usage.CompletionTokens,
		},
	}
	return json.Marshal(out)
}

// NewOpenAISSEToAnthropicReader translates an OpenAI chat completion SSE
// stream into the Anthropic Messages SSE event format.
func NewOpenAISSEToAnthropicReader(src io.ReadCloser, requestedModel string) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		defer src.Close()
		defer pw.Close()

		scanner := bufio.NewScanner(src)
		scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
		var msgID string
		var started, toolBlock bool
		var blockIndex int
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				if toolBlock {
					writeSSE(pw, "content_block_stop", map[string]any{"index": blockIndex})
				}
				writeSSE(pw, "message_delta", map[string]any{"delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{}})
				writeSSE(pw, "message_stop", map[string]any{})
				continue
			}
			var chunk struct {
				ID      string `json:"id"`
				Choices []struct {
					Delta struct {
						Content   string `json:"content"`
						ToolCalls []struct {
							Index    *int   `json:"index"`
							ID       string `json:"id"`
							Function struct {
								Name      string `json:"name"`
								Arguments string `json:"arguments"`
							} `json:"function"`
						} `json:"tool_calls"`
					} `json:"delta"`
					FinishReason string `json:"finish_reason"`
				} `json:"choices"`
			}
			if json.Unmarshal([]byte(data), &chunk) != nil {
				continue
			}
			if msgID == "" {
				msgID = chunk.ID
				if msgID == "" {
					msgID = fmt.Sprintf("msg_%d", time.Now().UnixNano())
				}
				writeSSE(pw, "message_start", map[string]any{
					"message": map[string]any{
						"id": msgID, "type": "message", "role": "assistant",
						"model": requestedModel, "content": []any{},
						"stop_reason": nil, "usage": map[string]any{"input_tokens": 0, "output_tokens": 0},
					},
				})
			}
			if len(chunk.Choices) == 0 {
				continue
			}
			choice := chunk.Choices[0]
			if choice.Delta.Content != "" {
				if toolBlock {
					writeSSE(pw, "content_block_stop", map[string]any{"index": blockIndex})
					toolBlock = false
				}
				if !started {
					writeSSE(pw, "content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
					started = true
				}
				writeSSE(pw, "content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": choice.Delta.Content}})
			}
			for _, tc := range choice.Delta.ToolCalls {
				if tc.Function.Name != "" {
					if started && !toolBlock {
						writeSSE(pw, "content_block_stop", map[string]any{"index": 0})
						started = false
					}
					blockIndex++
					toolBlock = true
					writeSSE(pw, "content_block_start", map[string]any{
						"index": blockIndex,
						"content_block": map[string]any{
							"type": "tool_use", "id": tc.ID, "name": tc.Function.Name, "input": map[string]any{},
						},
					})
				}
				if tc.Function.Arguments != "" && toolBlock {
					writeSSE(pw, "content_block_delta", map[string]any{"index": blockIndex, "delta": map[string]any{"type": "input_json_delta", "partial_json": tc.Function.Arguments}})
				}
			}
			if choice.FinishReason != "" && choice.FinishReason != "null" {
				reason := "end_turn"
				switch choice.FinishReason {
				case "tool_calls":
					reason = "tool_use"
				case "length":
					reason = "max_tokens"
				}
				if toolBlock {
					writeSSE(pw, "content_block_stop", map[string]any{"index": blockIndex})
					toolBlock = false
				} else if started {
					writeSSE(pw, "content_block_stop", map[string]any{"index": 0})
					started = false
				}
				writeSSE(pw, "message_delta", map[string]any{"delta": map[string]any{"stop_reason": reason}, "usage": map[string]any{}})
			}
		}
		// Fallback close if the upstream ended without [DONE].
		if toolBlock {
			writeSSE(pw, "content_block_stop", map[string]any{"index": blockIndex})
		}
		writeSSE(pw, "message_delta", map[string]any{"delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{}})
		writeSSE(pw, "message_stop", map[string]any{})
	}()
	return pr
}

func writeSSE(pw *io.PipeWriter, event string, data map[string]any) {
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(pw, "event: %s\ndata: %s\n\n", event, b)
}
