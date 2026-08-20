package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Rethinger/2papi/internal/adapter"
	"github.com/Rethinger/2papi/internal/config"
)

const (
	Name                  = "anthropic"
	DefaultAnthropicURL   = "https://api.anthropic.com"
	ClaudeAIBaseURL       = "https://claude.ai"
	AnthropicVersion      = "2023-06-01"
	oauthBeta             = "oauth-2025-04-20"
	claudeAIAPIPath       = "/api/organizations"
	maxAnthropicBodyBytes = 16 << 20
)

type Adapter struct {
	Client *http.Client
	auth   *tokenManager
}

func New(client *http.Client) *Adapter {
	if client == nil {
		client = &http.Client{Timeout: 0}
	}
	return &Adapter{Client: client}
}

// NewWithAuth enables OAuth token refresh (claude.ai browser login). The sink
// persists refreshed credentials to the control plane; refresh triggers a
// snapshot re-adoption on revision conflicts. A nil sink still allows
// in-memory refresh.
func NewWithAuth(client *http.Client, sink CredentialSink, refresh SnapshotRefreshTrigger) *Adapter {
	ad := New(client)
	ad.auth = newTokenManager(client, sink, refresh)
	return ad
}

func Register(reg *adapter.Registry, client *http.Client) error {
	return reg.Register(Name, New(client))
}

// RegisterWithAuth re-registers the anthropic adapter with OAuth token refresh
// enabled (used by the gateway once the control plane is reachable).
func RegisterWithAuth(reg *adapter.Registry, client *http.Client, sink CredentialSink, refresh SnapshotRefreshTrigger) error {
	return reg.Register(Name, NewWithAuth(client, sink, refresh))
}

// openAIChatRequest defines the subset of OpenAI chat completion payloads
// needed for translation to Anthropic Messages format.
type openAIChatRequest struct {
	Model       string          `json:"model"`
	Messages    []openAIMessage `json:"messages"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
	Stream      bool            `json:"stream,omitempty"`
	Tools       []openAITool    `json:"tools,omitempty"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    json.RawMessage  `json:"content"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openAITool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
	} `json:"function"`
}

// anthropicMessagesRequest is the payload expected by Anthropic /v1/messages.
type anthropicMessagesRequest struct {
	Model       string             `json:"model"`
	Messages    []anthropicMessage `json:"messages"`
	System      string             `json:"system,omitempty"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature *float64           `json:"temperature,omitempty"`
	TopP        *float64           `json:"top_p,omitempty"`
	Stream      bool               `json:"stream,omitempty"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

type anthropicToolResult struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
}

type anthropicToolUse struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

func (a *Adapter) Execute(ctx context.Context, ex adapter.Execution) (*adapter.Result, error) {
	if ex.Endpoint == adapter.EndpointCountTokens {
		return a.executeCountTokens(ctx, ex)
	}
	if ex.Endpoint != adapter.EndpointChatCompletions && ex.Endpoint != adapter.EndpointResponses {
		return nil, &adapter.CapabilityError{Kind: adapter.OperationKind(ex.Endpoint)}
	}

	// Claude.ai subscription accounts authenticate with browser cookies
	// (sessionKey) and talk to claude.ai's own web API, not /v1/messages.
	if ex.Account.Credential.Kind == "cookie" {
		return a.executeClaudeAI(ctx, ex)
	}

	anthropicPayload, stream, err := translateOpenAIToAnthropic(ex.Body, ex.Model.UpstreamModel)
	if err != nil {
		return nil, err
	}

	targetURL, err := joinAnthropicURL(ex.Account.BaseURL, "/v1/messages")
	if err != nil {
		return nil, err
	}

	// OAuth accounts (claude.ai browser login) may need a token refresh.
	cred := ex.Account.Credential
	if cred.Kind == "oauth" && a.auth != nil {
		fresh, _, _, err := a.auth.AccessToken(ctx, ex.Account, false)
		if err != nil {
			return nil, err
		}
		cred = fresh
	}

	buildRequest := func(authCred config.Credential) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(anthropicPayload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("anthropic-version", AnthropicVersion)
		req.Header.Set("content-type", "application/json")
		if stream {
			req.Header.Set("accept", "text/event-stream")
		}

		// OAuth credentials from claude.ai ("Connect to Claude Code")
		// authenticate against api.anthropic.com with a Bearer token and
		// require the OAuth beta plus the dangerous-direct-browser-access
		// header — the same headers the official Claude Code CLI sends.
		// Subscription spoof: make upstream think it's official CLI → lower quota burn
		if authCred.Kind == "oauth" {
			req.Header.Set("authorization", "Bearer "+authCred.AccessToken)
			req.Header.Set("anthropic-dangerous-direct-browser-access", "true")
			req.Header.Set("anthropic-beta", oauthBeta+",claude-code-20250219")
			req.Header.Set("x-app", "cli")
			req.Header.Set("User-Agent", "claude-code/1.0.100 (external, cli)")
			req.Header.Set("X-Stainless-Retry-Count", "0")
			req.Header.Set("X-Stainless-Timeout", "600")
			req.Header.Set("anthropic-billing-source", "claude_code")
		} else {
			req.Header.Set("x-api-key", resolveAPIKey(ex.Account))
			// Even for API key, spoof as CLI for family-aware routing
			req.Header.Set("User-Agent", "claude-code/1.0.100 (external, cli)")
			req.Header.Set("X-Stainless-Retry-Count", "0")
		}
		return req, nil
	}

	req, err := buildRequest(cred)
	if err != nil {
		return nil, err
	}

	resp, err := a.Client.Do(req)
	if err != nil {
		return nil, err
	}

	// 401 with an OAuth token means the token was revoked/expired server-side:
	// force a refresh and retry exactly once.
	if resp.StatusCode == http.StatusUnauthorized && cred.Kind == "oauth" && a.auth != nil {
		_ = resp.Body.Close()
		fresh, _, _, err := a.auth.AccessToken(ctx, ex.Account, true)
		if err != nil {
			return nil, err
		}
		req, err = buildRequest(fresh)
		if err != nil {
			return nil, err
		}
		resp, err = a.Client.Do(req)
		if err != nil {
			return nil, err
		}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &adapter.Result{
			Status: resp.StatusCode,
			Header: resp.Header.Clone(),
			Body:   resp.Body,
		}, nil
	}

	if stream {
		translatedReader := newAnthropicSSETranslator(resp.Body, ex.PublicModel)
		header := resp.Header.Clone()
		header.Set("Content-Type", "text/event-stream")
		return &adapter.Result{
			Status: http.StatusOK,
			Header: header,
			Body:   translatedReader,
		}, nil
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxAnthropicBodyBytes))
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}

	openAIJSON, err := translateAnthropicResponseToOpenAI(bodyBytes, ex.PublicModel)
	if err != nil {
		return nil, err
	}

	header := resp.Header.Clone()
	header.Set("Content-Type", "application/json")
	return &adapter.Result{
		Status: http.StatusOK,
		Header: header,
		Body:   io.NopCloser(bytes.NewReader(openAIJSON)),
	}, nil
}

func (a *Adapter) Operate(ctx context.Context, op adapter.Operation) (adapter.OperationResult, error) {
	switch op.Kind {
	case adapter.OperationDiscoverModels:
		return a.discoverModels(ctx, op.Account)
	case adapter.OperationValidateCredentials:
		return a.validateCredentials(ctx, op.Account)
	default:
		return adapter.OperationResult{}, &adapter.CapabilityError{Kind: op.Kind}
	}
}

func (a *Adapter) discoverModels(_ context.Context, _ config.Account) (adapter.OperationResult, error) {
	models := []map[string]any{
		{
			"slug":             "claude-opus-4-6",
			"display_name":     "Claude Opus 4.6",
			"visibility":       "list",
			"supported_in_api": true,
			"context_window":   200000,
			"capabilities": map[string]any{
				"tools":     true,
				"vision":    true,
				"reasoning": true,
			},
		},
		{
			"slug":             "claude-sonnet-4-6",
			"display_name":     "Claude Sonnet 4.6",
			"visibility":       "list",
			"supported_in_api": true,
			"context_window":   200000,
			"capabilities": map[string]any{
				"tools":     true,
				"vision":    true,
				"reasoning": true,
			},
		},
		{
			"slug":             "claude-opus-4-1-20250805",
			"display_name":     "Claude Opus 4.1",
			"visibility":       "list",
			"supported_in_api": true,
			"context_window":   200000,
			"capabilities": map[string]any{
				"tools":     true,
				"vision":    true,
				"reasoning": true,
			},
		},
		{
			"slug":             "claude-sonnet-4-5-20250929",
			"display_name":     "Claude Sonnet 4.5",
			"visibility":       "list",
			"supported_in_api": true,
			"context_window":   200000,
			"capabilities": map[string]any{
				"tools":     true,
				"vision":    true,
				"reasoning": true,
			},
		},
		{
			"slug":             "claude-haiku-4-5-20251001",
			"display_name":     "Claude Haiku 4.5",
			"visibility":       "list",
			"supported_in_api": true,
			"context_window":   200000,
			"capabilities": map[string]any{
				"tools":  true,
				"vision": true,
			},
		},
		{
			"slug":             "claude-3-7-sonnet-20250219",
			"display_name":     "Claude 3.7 Sonnet",
			"visibility":       "list",
			"supported_in_api": true,
			"context_window":   200000,
			"capabilities": map[string]any{
				"tools":     true,
				"vision":    true,
				"reasoning": true,
			},
		},
		{
			"slug":             "claude-3-5-sonnet-20241022",
			"display_name":     "Claude 3.5 Sonnet",
			"visibility":       "list",
			"supported_in_api": true,
			"context_window":   200000,
			"capabilities": map[string]any{
				"tools":  true,
				"vision": true,
			},
		},
		{
			"slug":             "claude-3-5-haiku-20241022",
			"display_name":     "Claude 3.5 Haiku",
			"visibility":       "list",
			"supported_in_api": true,
			"context_window":   200000,
			"capabilities": map[string]any{
				"tools":  true,
				"vision": true,
			},
		},
		{
			"slug":             "claude-3-opus-20240229",
			"display_name":     "Claude 3 Opus",
			"visibility":       "list",
			"supported_in_api": true,
			"context_window":   200000,
			"capabilities": map[string]any{
				"tools":  true,
				"vision": true,
			},
		},
	}
	data, err := json.Marshal(map[string]any{"models": models})
	if err != nil {
		return adapter.OperationResult{}, err
	}
	return adapter.OperationResult{Data: data}, nil
}

func (a *Adapter) validateCredentials(ctx context.Context, acct config.Account) (adapter.OperationResult, error) {
	switch acct.Credential.Kind {
	case "cookie":
		return a.validateClaudeAICredentials(ctx, acct)
	case "oauth":
		return a.validateOAuthCredentials(ctx, acct)
	default:
		return a.validateAPIKeyCredentials(ctx, acct)
	}
}

func (a *Adapter) validateAPIKeyCredentials(ctx context.Context, acct config.Account) (adapter.OperationResult, error) {
	targetURL, err := joinAnthropicURL(acct.BaseURL, "/v1/models")
	if err != nil {
		return adapter.OperationResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return adapter.OperationResult{}, err
	}
	apiKey := resolveAPIKey(acct)
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", AnthropicVersion)
	resp, err := a.Client.Do(req)
	if err != nil {
		return adapter.OperationResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return adapter.OperationResult{}, errors.New("anthropic credentials invalid")
	}
	return adapter.OperationResult{}, nil
}

func (a *Adapter) validateOAuthCredentials(ctx context.Context, acct config.Account) (adapter.OperationResult, error) {
	cred := acct.Credential
	if a.auth != nil {
		fresh, _, _, err := a.auth.AccessToken(ctx, acct, false)
		if err != nil {
			return adapter.OperationResult{}, err
		}
		cred = fresh
	}
	targetURL, err := joinAnthropicURL(acct.BaseURL, "/v1/models")
	if err != nil {
		return adapter.OperationResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return adapter.OperationResult{}, err
	}
	req.Header.Set("authorization", "Bearer "+cred.AccessToken)
	req.Header.Set("anthropic-dangerous-direct-browser-access", "true")
	req.Header.Set("anthropic-beta", oauthBeta)
	req.Header.Set("anthropic-version", AnthropicVersion)
	resp, err := a.Client.Do(req)
	if err != nil {
		return adapter.OperationResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return adapter.OperationResult{}, errors.New("anthropic oauth credentials invalid")
	}
	return adapter.OperationResult{}, nil
}

func (a *Adapter) validateClaudeAICredentials(ctx context.Context, acct config.Account) (adapter.OperationResult, error) {
	base := acct.BaseURL
	if base == "" {
		base = ClaudeAIBaseURL
	}
	orgID, err := a.fetchClaudeAIOrg(ctx, base, acct.Credential.Cookies)
	if err != nil {
		return adapter.OperationResult{}, fmt.Errorf("claude.ai cookie credentials invalid: %w", err)
	}
	if orgID == "" {
		return adapter.OperationResult{}, errors.New("claude.ai cookie credentials invalid: no organization")
	}
	return adapter.OperationResult{}, nil
}

func translateOpenAIToAnthropic(body []byte, upstreamModel string) ([]byte, bool, error) {
	var in openAIChatRequest
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, false, fmt.Errorf("invalid openai payload: %w", err)
	}

	model := in.Model
	if upstreamModel != "" {
		model = upstreamModel
	}

	maxTokens := in.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	var systemParts []string
	var anthropicMsgs []anthropicMessage

	for _, msg := range in.Messages {
		if msg.Role == "system" {
			var text string
			if err := json.Unmarshal(msg.Content, &text); err == nil && text != "" {
				systemParts = append(systemParts, text)
			}
			continue
		}

		if msg.Role == "user" {
			var text string
			if err := json.Unmarshal(msg.Content, &text); err == nil {
				anthropicMsgs = append(anthropicMsgs, anthropicMessage{
					Role:    "user",
					Content: text,
				})
			} else {
				anthropicMsgs = append(anthropicMsgs, anthropicMessage{
					Role:    "user",
					Content: msg.Content,
				})
			}
			continue
		}

		if msg.Role == "assistant" {
			if len(msg.ToolCalls) > 0 {
				var content []any
				var text string
				if err := json.Unmarshal(msg.Content, &text); err == nil && text != "" {
					content = append(content, map[string]any{"type": "text", "text": text})
				}
				for _, tc := range msg.ToolCalls {
					content = append(content, anthropicToolUse{
						Type:  "tool_use",
						ID:    tc.ID,
						Name:  tc.Function.Name,
						Input: json.RawMessage(tc.Function.Arguments),
					})
				}
				anthropicMsgs = append(anthropicMsgs, anthropicMessage{
					Role:    "assistant",
					Content: content,
				})
			} else {
				var text string
				_ = json.Unmarshal(msg.Content, &text)
				anthropicMsgs = append(anthropicMsgs, anthropicMessage{
					Role:    "assistant",
					Content: text,
				})
			}
			continue
		}

		if msg.Role == "tool" {
			var text string
			_ = json.Unmarshal(msg.Content, &text)
			toolResult := anthropicToolResult{
				Type:      "tool_result",
				ToolUseID: msg.ToolCallID,
				Content:   text,
			}
			anthropicMsgs = append(anthropicMsgs, anthropicMessage{
				Role:    "user",
				Content: []any{toolResult},
			})
			continue
		}
	}

	var tools []anthropicTool
	for _, t := range in.Tools {
		if t.Type == "function" && t.Function.Name != "" {
			tools = append(tools, anthropicTool{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				InputSchema: t.Function.Parameters,
			})
		}
	}

	req := anthropicMessagesRequest{
		Model:       model,
		Messages:    anthropicMsgs,
		System:      strings.Join(systemParts, "\n\n"),
		MaxTokens:   maxTokens,
		Temperature: in.Temperature,
		TopP:        in.TopP,
		Stream:      in.Stream,
		Tools:       tools,
	}

	out, err := json.Marshal(req)
	return out, in.Stream, err
}

func translateAnthropicResponseToOpenAI(body []byte, publicModel string) ([]byte, error) {
	var resp struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text,omitempty"`
			ID    string          `json:"id,omitempty"`
			Name  string          `json:"name,omitempty"`
			Input json.RawMessage `json:"input,omitempty"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}

	var textBuilder strings.Builder
	var toolCalls []map[string]any

	for _, c := range resp.Content {
		if c.Type == "text" {
			textBuilder.WriteString(c.Text)
		} else if c.Type == "tool_use" {
			toolCalls = append(toolCalls, map[string]any{
				"id":   c.ID,
				"type": "function",
				"function": map[string]any{
					"name":      c.Name,
					"arguments": string(c.Input),
				},
			})
		}
	}

	finishReason := "stop"
	if resp.StopReason == "tool_use" {
		finishReason = "tool_calls"
	} else if resp.StopReason == "max_tokens" {
		finishReason = "length"
	}

	message := map[string]any{
		"role":    "assistant",
		"content": textBuilder.String(),
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}

	out := map[string]any{
		"id":      resp.ID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   publicModel,
		"choices": []map[string]any{
			{
				"index":         0,
				"message":       message,
				"finish_reason": finishReason,
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     resp.Usage.InputTokens,
			"completion_tokens": resp.Usage.OutputTokens,
			"total_tokens":      resp.Usage.InputTokens + resp.Usage.OutputTokens,
		},
	}
	return json.Marshal(out)
}

func newAnthropicSSETranslator(src io.ReadCloser, publicModel string) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		defer src.Close()
		defer pw.Close()

		scanner := bufio.NewScanner(src)
		var currentEvent string
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event: ") {
				currentEvent = strings.TrimPrefix(line, "event: ")
				continue
			}
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "" {
				continue
			}

			switch currentEvent {
			case "content_block_delta":
				var delta struct {
					Delta struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"delta"`
				}
				if json.Unmarshal([]byte(data), &delta) == nil && delta.Delta.Text != "" {
					chunk := map[string]any{
						"id":      "chatcmpl-" + strconv.FormatInt(time.Now().UnixNano(), 36),
						"object":  "chat.completion.chunk",
						"created": time.Now().Unix(),
						"model":   publicModel,
						"choices": []map[string]any{
							{
								"index": 0,
								"delta": map[string]any{"content": delta.Delta.Text},
							},
						},
					}
					chunkBytes, _ := json.Marshal(chunk)
					_, _ = fmt.Fprintf(pw, "data: %s\n\n", chunkBytes)
				}
			case "message_stop":
				_, _ = fmt.Fprint(pw, "data: [DONE]\n\n")
			}
		}
		_, _ = fmt.Fprint(pw, "data: [DONE]\n\n")
	}()
	return pr
}

func joinAnthropicURL(base, path string) (string, error) {
	if base == "" {
		base = DefaultAnthropicURL
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	return u.String(), nil
}

func resolveAPIKey(acct config.Account) string {
	if acct.Credential.APIKey != "" {
		return acct.Credential.APIKey
	}
	return acct.APIKey
}
