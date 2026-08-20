package gemini

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
	"strings"
	"time"

	"github.com/Rethinger/2papi/internal/adapter"
	"github.com/Rethinger/2papi/internal/config"
)

const (
	Name               = "gemini"
	DefaultGeminiBase  = "https://generativelanguage.googleapis.com"
	maxGeminiBodyBytes = 16 << 20
)

type Adapter struct {
	Client *http.Client
}

func New(client *http.Client) *Adapter {
	if client == nil {
		client = &http.Client{Timeout: 0}
	}
	return &Adapter{Client: client}
}

func Register(reg *adapter.Registry, client *http.Client) error {
	return reg.Register(Name, New(client))
}

type openAIChatPayload struct {
	Model       string          `json:"model"`
	Messages    []openAIMessage `json:"messages"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
	Stream      bool            `json:"stream,omitempty"`
}

type openAIMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type geminiRequest struct {
	Contents          []geminiContent   `json:"contents"`
	SystemInstruction *geminiContent    `json:"system_instruction,omitempty"`
	GenerationConfig  *generationConfig `json:"generationConfig,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text,omitempty"`
}

type generationConfig struct {
	MaxOutputTokens int      `json:"maxOutputTokens,omitempty"`
	Temperature     *float64 `json:"temperature,omitempty"`
	TopP            *float64 `json:"topP,omitempty"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Role  string       `json:"role"`
			Parts []geminiPart `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int64 `json:"promptTokenCount"`
		CandidatesTokenCount int64 `json:"candidatesTokenCount"`
		TotalTokenCount      int64 `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}

func (a *Adapter) Execute(ctx context.Context, ex adapter.Execution) (*adapter.Result, error) {
	if ex.Endpoint != adapter.EndpointChatCompletions && ex.Endpoint != adapter.EndpointResponses {
		return nil, &adapter.CapabilityError{Kind: adapter.OperationKind(ex.Endpoint)}
	}

	geminiPayload, stream, err := translateOpenAIToGemini(ex.Body)
	if err != nil {
		return nil, err
	}

	model := ex.Model.UpstreamModel
	if model == "" {
		model = "gemini-2.0-flash"
	}

	action := "generateContent"
	if stream {
		action = "streamGenerateContent"
	}

	targetURL, err := buildGeminiURL(ex.Account.BaseURL, model, action, stream, resolveAPIKey(ex.Account))
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(geminiPayload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json")
	if stream {
		req.Header.Set("accept", "text/event-stream")
	}

	resp, err := a.Client.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &adapter.Result{
			Status: resp.StatusCode,
			Header: resp.Header.Clone(),
			Body:   resp.Body,
		}, nil
	}

	if stream {
		header := resp.Header.Clone()
		header.Set("Content-Type", "text/event-stream")
		return &adapter.Result{
			Status: http.StatusOK,
			Header: header,
			Body:   newGeminiSSETranslator(resp.Body, ex.PublicModel),
		}, nil
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxGeminiBodyBytes))
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}

	openAIJSON, err := translateGeminiResponseToOpenAI(bodyBytes, ex.PublicModel)
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
			"slug":             "gemini-2.5-flash",
			"display_name":     "Gemini 2.5 Flash",
			"visibility":       "list",
			"supported_in_api": true,
			"context_window":   1000000,
			"capabilities": map[string]any{
				"tools":  true,
				"vision": true,
			},
		},
		{
			"slug":             "gemini-2.5-pro",
			"display_name":     "Gemini 2.5 Pro",
			"visibility":       "list",
			"supported_in_api": true,
			"context_window":   2000000,
			"capabilities": map[string]any{
				"tools":     true,
				"vision":    true,
				"reasoning": true,
			},
		},
		{
			"slug":             "gemini-2.0-flash",
			"display_name":     "Gemini 2.0 Flash",
			"visibility":       "list",
			"supported_in_api": true,
			"context_window":   1000000,
			"capabilities": map[string]any{
				"tools":  true,
				"vision": true,
			},
		},
		{
			"slug":             "gemini-1.5-pro",
			"display_name":     "Gemini 1.5 Pro",
			"visibility":       "list",
			"supported_in_api": true,
			"context_window":   2000000,
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
	base := acct.BaseURL
	if base == "" {
		base = DefaultGeminiBase
	}
	targetURL := fmt.Sprintf("%s/v1beta/models?key=%s", strings.TrimRight(base, "/"), url.QueryEscape(resolveAPIKey(acct)))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return adapter.OperationResult{}, err
	}
	resp, err := a.Client.Do(req)
	if err != nil {
		return adapter.OperationResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return adapter.OperationResult{}, errors.New("gemini credentials invalid")
	}
	return adapter.OperationResult{}, nil
}

func translateOpenAIToGemini(body []byte) ([]byte, bool, error) {
	var in openAIChatPayload
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, false, fmt.Errorf("invalid openai payload: %w", err)
	}

	var contents []geminiContent
	var systemInstruction *geminiContent

	for _, msg := range in.Messages {
		var text string
		_ = json.Unmarshal(msg.Content, &text)

		if msg.Role == "system" {
			systemInstruction = &geminiContent{
				Parts: []geminiPart{{Text: text}},
			}
			continue
		}

		role := "user"
		if msg.Role == "assistant" {
			role = "model"
		}

		contents = append(contents, geminiContent{
			Role:  role,
			Parts: []geminiPart{{Text: text}},
		})
	}

	var genConfig *generationConfig
	if in.MaxTokens > 0 || in.Temperature != nil || in.TopP != nil {
		genConfig = &generationConfig{
			MaxOutputTokens: in.MaxTokens,
			Temperature:     in.Temperature,
			TopP:            in.TopP,
		}
	}

	req := geminiRequest{
		Contents:          contents,
		SystemInstruction: systemInstruction,
		GenerationConfig:  genConfig,
	}

	out, err := json.Marshal(req)
	return out, in.Stream, err
}

func translateGeminiResponseToOpenAI(body []byte, publicModel string) ([]byte, error) {
	var resp geminiResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}

	var textBuilder strings.Builder
	finishReason := "stop"
	if len(resp.Candidates) > 0 {
		for _, part := range resp.Candidates[0].Content.Parts {
			textBuilder.WriteString(part.Text)
		}
		if resp.Candidates[0].FinishReason == "MAX_TOKENS" {
			finishReason = "length"
		}
	}

	out := map[string]any{
		"id":      fmt.Sprintf("chatcmpl-gemini-%d", time.Now().UnixNano()),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   publicModel,
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": textBuilder.String(),
				},
				"finish_reason": finishReason,
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     resp.UsageMetadata.PromptTokenCount,
			"completion_tokens": resp.UsageMetadata.CandidatesTokenCount,
			"total_tokens":      resp.UsageMetadata.TotalTokenCount,
		},
	}
	return json.Marshal(out)
}

func newGeminiSSETranslator(src io.ReadCloser, publicModel string) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		defer src.Close()
		defer pw.Close()

		scanner := bufio.NewScanner(src)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "" || data == "[DONE]" {
				continue
			}

			var resp geminiResponse
			if json.Unmarshal([]byte(data), &resp) == nil && len(resp.Candidates) > 0 {
				var text string
				for _, part := range resp.Candidates[0].Content.Parts {
					text += part.Text
				}
				if text != "" {
					chunk := map[string]any{
						"id":      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
						"object":  "chat.completion.chunk",
						"created": time.Now().Unix(),
						"model":   publicModel,
						"choices": []map[string]any{
							{
								"index": 0,
								"delta": map[string]any{"content": text},
							},
						},
					}
					chunkBytes, _ := json.Marshal(chunk)
					_, _ = fmt.Fprintf(pw, "data: %s\n\n", chunkBytes)
				}
			}
		}
		_, _ = fmt.Fprint(pw, "data: [DONE]\n\n")
	}()
	return pr
}

func buildGeminiURL(base, model, action string, stream bool, apiKey string) (string, error) {
	if base == "" {
		base = DefaultGeminiBase
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	u.Path = fmt.Sprintf("/v1beta/models/%s:%s", url.PathEscape(model), action)
	q := u.Query()
	if apiKey != "" {
		q.Set("key", apiKey)
	}
	if stream {
		q.Set("alt", "sse")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func resolveAPIKey(acct config.Account) string {
	if acct.Credential.APIKey != "" {
		return acct.Credential.APIKey
	}
	return acct.APIKey
}
