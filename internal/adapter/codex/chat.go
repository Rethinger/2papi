package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Rethinger/2papi/internal/adapter"
)

const maxChatResponseBytes = 16 << 20

type chatRequest struct {
	Model               string          `json:"model"`
	Messages            []chatMessage   `json:"messages"`
	Tools               []chatTool      `json:"tools,omitempty"`
	ToolChoice          json.RawMessage `json:"tool_choice,omitempty"`
	MaxCompletionTokens int             `json:"max_completion_tokens,omitempty"`
	MaxTokens           int             `json:"max_tokens,omitempty"`
	ReasoningEffort     string          `json:"reasoning_effort,omitempty"`
	Stream              bool            `json:"stream,omitempty"`
	Stop                json.RawMessage `json:"stop,omitempty"`
	ResponseFormat      json.RawMessage `json:"response_format,omitempty"`
	Functions           json.RawMessage `json:"functions,omitempty"`
	FunctionCall        json.RawMessage `json:"function_call,omitempty"`
}

type chatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolCalls  []chatToolCall  `json:"tool_calls,omitempty"`
}

type chatToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Arguments   string          `json:"arguments,omitempty"`
}

type chatTool struct {
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type responsesRequest struct {
	Model           string              `json:"model"`
	Instructions    string              `json:"instructions,omitempty"`
	Input           []responsesInput    `json:"input"`
	Tools           []responsesTool     `json:"tools,omitempty"`
	ToolChoice      json.RawMessage     `json:"tool_choice,omitempty"`
	Reasoning       *responsesReasoning `json:"reasoning,omitempty"`
	Stop            json.RawMessage     `json:"stop,omitempty"`
	Stream          bool                `json:"stream"`
	Store           bool                `json:"store"`
}

type responsesReasoning struct {
	Effort string `json:"effort"`
}

type responsesInput struct {
	Type      string             `json:"type"`
	Role      string             `json:"role,omitempty"`
	Content   []responsesContent `json:"content,omitempty"`
	ID        string             `json:"id,omitempty"`
	CallID    string             `json:"call_id,omitempty"`
	Name      string             `json:"name,omitempty"`
	Arguments string             `json:"arguments,omitempty"`
	Output    string             `json:"output,omitempty"`
}

type responsesContent struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL string `json:"image_url,omitempty"`
}

type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type chatContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url,omitempty"`
}

func (a *Adapter) executeChat(ctx context.Context, ex adapter.Execution) (*adapter.Result, error) {
	body, err := convertChatRequest(ex.Body, ex.Model.UpstreamModel)
	if err != nil {
		return nil, err
	}
	converted := ex
	converted.Endpoint = adapter.EndpointResponses
	converted.Body = body
	result, err := a.executeResponses(ctx, converted)
	if err != nil || result == nil || result.Body == nil {
		return result, err
	}
	if result.Status < http.StatusOK || result.Status >= http.StatusMultipleChoices {
		return result, nil
	}
	if isStreamingRequest(body) {
		result.Body = convertResponsesSSEToChat(result.Body, ex.PublicModel)
		result.Header.Set("Content-Type", "text/event-stream")
		result.Header.Del("Content-Length")
		return result, nil
	}
	defer result.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(result.Body, maxChatResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxChatResponseBytes {
		return nil, fmt.Errorf("upstream response body exceeds limit")
	}
	convertedBody, err := convertResponsesFinalToChat(raw, ex.PublicModel)
	if err != nil {
		return nil, err
	}
	result.Body = io.NopCloser(bytes.NewReader(convertedBody))
	result.Header.Set("Content-Type", "application/json")
	result.Header.Set("Content-Length", strconv.Itoa(len(convertedBody)))
	return result, nil
}

func convertChatRequest(raw []byte, upstreamModel string) ([]byte, error) {
	var in chatRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&in); err != nil {
		return nil, fmt.Errorf("invalid chat request: %w", err)
	}
	if err := ensureJSONEOF(dec); err != nil {
		return nil, err
	}
	if upstreamModel == "" || in.Model == "" || len(in.Messages) == 0 {
		return nil, fmt.Errorf("invalid chat request: model and messages required")
	}
	if len(in.ResponseFormat) > 0 && !bytes.Equal(bytes.TrimSpace(in.ResponseFormat), []byte("null")) {
		return nil, unsupportedChatFeature("response_format")
	}
	if len(in.Functions) > 0 || len(in.FunctionCall) > 0 {
		return nil, unsupportedChatFeature("legacy functions")
	}
	out := responsesRequest{Model: upstreamModel, Input: []responsesInput{}, Stream: in.Stream, Store: false, Stop: in.Stop}
	var instructions []string
	for _, message := range in.Messages {
		switch message.Role {
		case "system", "developer":
			text, err := chatMessageText(message.Content)
			if err != nil {
				return nil, err
			}
			if text != "" {
				instructions = append(instructions, text)
			}
		case "user":
			text, images, err := chatMessageParts(message.Content)
			if err != nil {
				return nil, err
			}
			content := []responsesContent{}
			if text != "" {
				content = append(content, responsesContent{Type: "input_text", Text: text})
			}
			for _, url := range images {
				content = append(content, responsesContent{Type: "input_image", ImageURL: url})
			}
			if len(content) > 0 {
				out.Input = append(out.Input, responsesInput{Type: "message", Role: "user", Content: content})
			}
			for _, call := range message.ToolCalls {
				if call.Type != "function" || call.ID == "" || call.Function.Name == "" {
					return nil, fmt.Errorf("invalid chat request: malformed tool call")
				}
				out.Input = append(out.Input, responsesInput{Type: "function_call", ID: call.ID, CallID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments})
			}
		case "assistant":
			text, err := chatMessageText(message.Content)
			if err != nil {
				return nil, err
			}
			if text != "" {
				out.Input = append(out.Input, responsesInput{Type: "message", Role: message.Role, Content: []responsesContent{{Type: "output_text", Text: text}}})
			}
			for _, call := range message.ToolCalls {
				if call.Type != "function" || call.ID == "" || call.Function.Name == "" {
					return nil, fmt.Errorf("invalid chat request: malformed tool call")
				}
				out.Input = append(out.Input, responsesInput{Type: "function_call", ID: call.ID, CallID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments})
			}
		case "tool":
			text, err := chatMessageText(message.Content)
			if err != nil {
				return nil, err
			}
			if message.ToolCallID == "" {
				return nil, fmt.Errorf("invalid chat request: tool_call_id required")
			}
			out.Input = append(out.Input, responsesInput{Type: "function_call_output", CallID: message.ToolCallID, Output: text})
		default:
			return nil, unsupportedChatFeature("message role " + message.Role)
		}
	}
	out.Instructions = strings.Join(instructions, "\n")
	for _, tool := range in.Tools {
		if tool.Type != "function" || tool.Function.Name == "" {
			return nil, unsupportedChatFeature("tool type " + tool.Type)
		}
		out.Tools = append(out.Tools, responsesTool{Type: "function", Name: tool.Function.Name, Description: tool.Function.Description, Parameters: tool.Function.Parameters})
	}
	choice, err := convertToolChoice(in.ToolChoice)
	if err != nil {
		return nil, err
	}
	out.ToolChoice = choice
	// The Chat Completions compatibility endpoint accepts both token-limit
	// fields, but the Codex Responses backend rejects max_output_tokens.
	// Keep the client request compatible by intentionally omitting the limit.
	if in.ReasoningEffort != "" {
		switch in.ReasoningEffort {
		case "minimal", "low", "medium", "high", "xhigh":
			out.Reasoning = &responsesReasoning{Effort: in.ReasoningEffort}
		default:
			return nil, fmt.Errorf("invalid chat request: reasoning_effort")
		}
	}
	return json.Marshal(out)
}

func ensureJSONEOF(dec *json.Decoder) error {
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("invalid chat request: trailing tokens")
	}
	return nil
}

func unsupportedChatFeature(field string) error {
	return fmt.Errorf("codex_feature_unsupported: %s", field)
}

func chatMessageText(raw json.RawMessage) (string, error) {
	text, _, err := chatMessageParts(raw)
	return text, err
}

// chatMessageParts extracts the plain text and the image URLs from an OpenAI
// message content value, which may be a plain string or an array of content
// parts. Image parts are only meaningful on user messages; every other role
// keeps rejecting them via chatMessageText.
func chatMessageParts(raw json.RawMessage) (string, []string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil, nil
	}
	var text string
	if json.Unmarshal(trimmed, &text) == nil {
		return text, nil, nil
	}
	var parts []chatContentPart
	if err := json.Unmarshal(trimmed, &parts); err != nil {
		return "", nil, fmt.Errorf("invalid chat request: message content")
	}
	var b strings.Builder
	var images []string
	for _, part := range parts {
		switch part.Type {
		case "text":
			b.WriteString(part.Text)
		case "image_url":
			if part.ImageURL == nil || strings.TrimSpace(part.ImageURL.URL) == "" {
				return "", nil, fmt.Errorf("invalid chat request: image_url part requires url")
			}
			images = append(images, part.ImageURL.URL)
		default:
			return "", nil, unsupportedChatFeature("message content " + part.Type)
		}
	}
	return b.String(), images, nil
}

func convertToolChoice(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var simple string
	if json.Unmarshal(trimmed, &simple) == nil {
		switch simple {
		case "none", "auto", "required":
			return append(json.RawMessage(nil), trimmed...), nil
		default:
			return nil, unsupportedChatFeature("tool_choice " + simple)
		}
	}
	var choice struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(trimmed, &choice); err != nil || choice.Type != "function" || choice.Function.Name == "" {
		return nil, fmt.Errorf("invalid chat request: tool_choice")
	}
	return json.Marshal(struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}{Type: "function", Name: choice.Function.Name})
}

type responsesFinal struct {
	ID                string                `json:"id"`
	Model             string                `json:"model"`
	CreatedAt         int64                 `json:"created_at"`
	Status            string                `json:"status"`
	Output            []responsesOutputItem `json:"output"`
	Usage             responsesUsage        `json:"usage"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details,omitempty"`
}

type responsesOutputItem struct {
	Type      string                   `json:"type"`
	Role      string                   `json:"role,omitempty"`
	Content   []responsesOutputContent `json:"content,omitempty"`
	ID        string                   `json:"id,omitempty"`
	CallID    string                   `json:"call_id,omitempty"`
	Name      string                   `json:"name,omitempty"`
	Arguments string                   `json:"arguments,omitempty"`
}

type responsesOutputContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type responsesUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type chatCompletion struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []chatChoice `json:"choices"`
	Usage   chatUsage    `json:"usage"`
}

type chatChoice struct {
	Index        int            `json:"index"`
	Message      chatOutMessage `json:"message"`
	FinishReason string         `json:"finish_reason"`
}

type chatOutMessage struct {
	Role      string            `json:"role"`
	Content   string            `json:"content"`
	ToolCalls []chatOutToolCall `json:"tool_calls,omitempty"`
}

type chatOutToolCall struct {
	Index    *int            `json:"index,omitempty"`
	ID       string          `json:"id,omitempty"`
	Type     string          `json:"type,omitempty"`
	Function chatOutFunction `json:"function"`
}

type chatOutFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func convertResponsesFinalToChat(raw []byte, publicModel string) ([]byte, error) {
	var in responsesFinal
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&in); err != nil {
		return nil, fmt.Errorf("invalid Codex response: %w", err)
	}
	message := chatOutMessage{Role: "assistant"}
	for _, item := range in.Output {
		switch item.Type {
		case "message":
			for _, content := range item.Content {
				if content.Type == "output_text" {
					message.Content += content.Text
				}
			}
		case "function_call":
			id := item.CallID
			if id == "" {
				id = item.ID
			}
			message.ToolCalls = append(message.ToolCalls, chatOutToolCall{ID: id, Type: "function", Function: chatOutFunction{Name: item.Name, Arguments: item.Arguments}})
		}
	}
	finish := responseFinishReason(in)
	created := in.CreatedAt
	if created == 0 {
		created = time.Now().Unix()
	}
	id := in.ID
	if !strings.HasPrefix(id, "chatcmpl-") {
		id = "chatcmpl-" + id
	}
	out := chatCompletion{ID: id, Object: "chat.completion", Created: created, Model: publicModel, Choices: []chatChoice{{Index: 0, Message: message, FinishReason: finish}}, Usage: chatUsage{PromptTokens: in.Usage.InputTokens, CompletionTokens: in.Usage.OutputTokens, TotalTokens: in.Usage.TotalTokens}}
	return json.Marshal(out)
}

func responseFinishReason(in responsesFinal) string {
	for _, item := range in.Output {
		if item.Type == "function_call" {
			return "tool_calls"
		}
	}
	if in.Status == "incomplete" && in.IncompleteDetails != nil && in.IncompleteDetails.Reason == "max_output_tokens" {
		return "length"
	}
	return "stop"
}

type responsesStreamEvent struct {
	Type        string              `json:"type"`
	Delta       string              `json:"delta,omitempty"`
	OutputIndex int                 `json:"output_index,omitempty"`
	Item        responsesOutputItem `json:"item,omitempty"`
	Response    responsesFinal      `json:"response,omitempty"`
}

type chatChunk struct {
	ID      string            `json:"id"`
	Object  string            `json:"object"`
	Created int64             `json:"created"`
	Model   string            `json:"model"`
	Choices []chatChunkChoice `json:"choices"`
	Usage   *chatUsage        `json:"usage,omitempty"`
}

type chatChunkChoice struct {
	Index        int            `json:"index"`
	Delta        chatChunkDelta `json:"delta"`
	FinishReason *string        `json:"finish_reason"`
}

type chatChunkDelta struct {
	Role      string            `json:"role,omitempty"`
	Content   string            `json:"content,omitempty"`
	ToolCalls []chatOutToolCall `json:"tool_calls,omitempty"`
}

func convertResponsesSSEToChat(r io.ReadCloser, publicModel string) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		defer r.Close()
		defer pw.Close()
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		id := "chatcmpl-codex"
		created := time.Now().Unix()
		roleSent := false
		toolIndexes := map[int]int{}
		nextToolIndex := 0
		writeChunk := func(delta chatChunkDelta, finish *string, usage *chatUsage) bool {
			chunk := chatChunk{ID: id, Object: "chat.completion.chunk", Created: created, Model: publicModel, Choices: []chatChunkChoice{{Index: 0, Delta: delta, FinishReason: finish}}, Usage: usage}
			b, err := json.Marshal(chunk)
			if err != nil {
				return false
			}
			_, err = fmt.Fprintf(pw, "data: %s\n\n", b)
			return err == nil
		}
		for scanner.Scan() {
			data, ok := sseDataPayload(scanner.Bytes())
			if !ok || bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
				continue
			}
			var event responsesStreamEvent
			if json.Unmarshal(data, &event) != nil {
				continue
			}
			if event.Response.ID != "" {
				id = event.Response.ID
				if !strings.HasPrefix(id, "chatcmpl-") {
					id = "chatcmpl-" + id
				}
			}
			if event.Response.CreatedAt != 0 {
				created = event.Response.CreatedAt
			}
			if !roleSent && event.Type == "response.created" {
				if !writeChunk(chatChunkDelta{Role: "assistant"}, nil, nil) {
					return
				}
				roleSent = true
				continue
			}
			switch event.Type {
			case "response.output_text.delta":
				if !roleSent {
					if !writeChunk(chatChunkDelta{Role: "assistant"}, nil, nil) {
						return
					}
					roleSent = true
				}
				if !writeChunk(chatChunkDelta{Content: event.Delta}, nil, nil) {
					return
				}
			case "response.output_item.added":
				if event.Item.Type != "function_call" {
					continue
				}
				idx := nextToolIndex
				nextToolIndex++
				toolIndexes[event.OutputIndex] = idx
				callID := event.Item.CallID
				if callID == "" {
					callID = event.Item.ID
				}
				if !writeChunk(chatChunkDelta{ToolCalls: []chatOutToolCall{{Index: &idx, ID: callID, Type: "function", Function: chatOutFunction{Name: event.Item.Name}}}}, nil, nil) {
					return
				}
			case "response.function_call_arguments.delta":
				idx, ok := toolIndexes[event.OutputIndex]
				if !ok {
					idx = nextToolIndex
					toolIndexes[event.OutputIndex] = idx
					nextToolIndex++
				}
				if !writeChunk(chatChunkDelta{ToolCalls: []chatOutToolCall{{Index: &idx, Function: chatOutFunction{Arguments: event.Delta}}}}, nil, nil) {
					return
				}
			case "response.completed", "response.incomplete", "response.failed":
				finish := responseFinishReason(event.Response)
				usage := &chatUsage{PromptTokens: event.Response.Usage.InputTokens, CompletionTokens: event.Response.Usage.OutputTokens, TotalTokens: event.Response.Usage.TotalTokens}
				if !writeChunk(chatChunkDelta{}, &finish, usage) {
					return
				}
			}
		}
		if err := scanner.Err(); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		_, _ = io.WriteString(pw, "data: [DONE]\n\n")
	}()
	return pr
}
