package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Rethinger/2papi/internal/adapter"
	"github.com/Rethinger/2papi/internal/config"
)

const (
	nilMessageUUID = "00000000-0000-4000-8000-000000000000"
	// claudeAIBrowserUA mirrors a current Chrome user agent; claude.ai
	// fingerprinting rejects bare Go clients.
	claudeAIBrowserUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/135.0.0.0 Safari/537.36"
)

// claudeAICompletionRequest mirrors the payload claude.ai's web UI posts to
// /api/organizations/{org}/chat_conversations/{uuid}/completion. The prompt
// field carries the full conversation transcript as text.
type claudeAICompletionRequest struct {
	Attachments        []any             `json:"attachments"`
	Files              []string          `json:"files"`
	Locale             string            `json:"locale"`
	Model              string            `json:"model"`
	PersonalizedStyles []map[string]any  `json:"personalized_styles"`
	Prompt             string            `json:"prompt"`
	RenderingMode      string            `json:"rendering_mode"`
	SyncSources        []string          `json:"sync_sources"`
	Timezone           string            `json:"timezone"`
	Tools              []map[string]any  `json:"tools"`
	TurnMessageUUIDs   map[string]string `json:"turn_message_uuids"`
	// CreateConversation is set on the first message of a fresh conversation.
	CreateConversation map[string]any `json:"create_conversation_params,omitempty"`
	ParentMessageUUID  string         `json:"parent_message_uuid,omitempty"`
}

func claudeAIDefaultStyle() map[string]any {
	return map[string]any{
		"isDefault":  true,
		"key":        "Default",
		"name":       "Normal",
		"nameKey":    "normal_style_name",
		"prompt":     "Normal\n",
		"summary":    "Default responses from Claude",
		"summaryKey": "normal_style_summary",
		"type":       "default",
	}
}

// randomUUID returns a random v4 UUID without external dependencies.
func randomUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// executeCountTokens forwards an Anthropic /v1/messages/count_tokens request
// to the upstream. Cookie-authenticated claude.ai accounts have no token
// counting endpoint and fail with a capability error.
func (a *Adapter) executeCountTokens(ctx context.Context, ex adapter.Execution) (*adapter.Result, error) {
	if ex.Account.Credential.Kind == "cookie" {
		return nil, &adapter.CapabilityError{Kind: adapter.OperationKind(ex.Endpoint)}
	}
	cred := ex.Account.Credential
	if cred.Kind == "oauth" && a.auth != nil {
		fresh, _, _, err := a.auth.AccessToken(ctx, ex.Account, false)
		if err != nil {
			return nil, err
		}
		cred = fresh
	}
	targetURL, err := joinAnthropicURL(ex.Account.BaseURL, "/v1/messages/count_tokens")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(ex.Body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("anthropic-version", AnthropicVersion)
	req.Header.Set("content-type", "application/json")
	if cred.Kind == "oauth" {
		req.Header.Set("authorization", "Bearer "+cred.AccessToken)
		req.Header.Set("anthropic-dangerous-direct-browser-access", "true")
		req.Header.Set("anthropic-beta", oauthBeta)
		req.Header.Set("x-app", "cli")
	} else {
		req.Header.Set("x-api-key", resolveAPIKey(ex.Account))
	}
	resp, err := a.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &adapter.Result{Status: resp.StatusCode, Header: resp.Header.Clone(), Body: resp.Body}, nil
	}
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxAnthropicBodyBytes))
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	header := resp.Header.Clone()
	header.Set("Content-Type", "application/json")
	return &adapter.Result{Status: http.StatusOK, Header: header, Body: io.NopCloser(bytes.NewReader(bodyBytes))}, nil
}

// executeClaudeAI runs a chat completion through claude.ai's web API using a
// browser session cookie (sessionKey). Every gateway request maps to a fresh,
// temporary claude.ai conversation, so stateless OpenAI chat semantics hold.
func (a *Adapter) executeClaudeAI(ctx context.Context, ex adapter.Execution) (*adapter.Result, error) {
	base := ex.Account.BaseURL
	if base == "" {
		base = ClaudeAIBaseURL
	}
	cred := ex.Account.Credential

	orgID, err := a.resolveClaudeAIOrg(ctx, base, cred)
	if err != nil {
		return nil, err
	}

	payload, stream, err := translateOpenAIToClaudeAI(ex.Body, ex.Model.UpstreamModel)
	if err != nil {
		return nil, err
	}

	targetURL := fmt.Sprintf("%s%s/%s/chat_conversations/%s/completion",
		strings.TrimRight(base, "/"), claudeAIAPIPath, orgID, randomUUID())

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "text/event-stream")
	req.Header.Set("cookie", cred.Cookies)
	req.Header.Set("anthropic-device-id", randomUUID())
	req.Header.Set("x-activity-session-id", randomUUID())
	req.Header.Set("user-agent", claudeAIBrowserUA)
	req.Header.Set("origin", base)
	req.Header.Set("referer", base+"/new")

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
		translatedReader := newAnthropicSSETranslator(resp.Body, ex.PublicModel)
		header := resp.Header.Clone()
		header.Set("Content-Type", "text/event-stream")
		return &adapter.Result{
			Status: http.StatusOK,
			Header: header,
			Body:   translatedReader,
		}, nil
	}

	// claude.ai always streams; aggregate the SSE events into an Anthropic
	// message and reuse the standard response translator.
	anthropicJSON, err := aggregateClaudeAIStream(resp.Body)
	if err != nil {
		return nil, err
	}
	openAIJSON, err := translateAnthropicResponseToOpenAI(anthropicJSON, ex.PublicModel)
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

// resolveClaudeAIOrg returns the account's organization UUID, discovering it
// from /api/organizations when not pinned on the credential.
func (a *Adapter) resolveClaudeAIOrg(ctx context.Context, base string, cred config.Credential) (string, error) {
	if cred.OrganizationID != "" {
		return cred.OrganizationID, nil
	}
	return a.fetchClaudeAIOrg(ctx, base, cred.Cookies)
}

func (a *Adapter) fetchClaudeAIOrg(ctx context.Context, base, cookies string) (string, error) {
	target := strings.TrimRight(base, "/") + claudeAIAPIPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("cookie", cookies)
	req.Header.Set("user-agent", claudeAIBrowserUA)
	req.Header.Set("origin", base)
	resp, err := a.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("claude.ai organizations returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var out struct {
		Organizations []struct {
			UUID string `json:"uuid"`
		} `json:"organizations"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	for _, org := range out.Organizations {
		if org.UUID != "" {
			return org.UUID, nil
		}
	}
	return "", errors.New("claude.ai returned no organizations")
}

// translateOpenAIToClaudeAI converts an OpenAI chat payload into the claude.ai
// web completion payload. OpenAI turns are flattened into the transcript the
// claude.ai UI uses: "Human: …" / "Assistant: …" blocks, with the system
// prompt prepended to the first human turn and tool results inlined as
// [tool_result] text.
func translateOpenAIToClaudeAI(body []byte, upstreamModel string) ([]byte, bool, error) {
	var in openAIChatRequest
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, false, fmt.Errorf("invalid openai payload: %w", err)
	}

	model := in.Model
	if upstreamModel != "" {
		model = upstreamModel
	}

	var systemParts []string
	type turn struct {
		prefix string
		text   string
	}
	var turns []turn
	appendTurn := func(prefix, text string) {
		if text == "" {
			return
		}
		turns = append(turns, turn{prefix: prefix, text: text})
	}

	for _, msg := range in.Messages {
		switch msg.Role {
		case "system":
			if text := messageText(msg.Content); text != "" {
				systemParts = append(systemParts, text)
			}
		case "user":
			appendTurn("Human: ", messageText(msg.Content))
		case "assistant":
			text := messageText(msg.Content)
			if text == "" && len(msg.ToolCalls) > 0 {
				text = "(tool call requested)"
			}
			appendTurn("Assistant: ", text)
		case "tool":
			appendTurn("Human: ", "[tool_result]\n"+messageText(msg.Content))
		}
	}

	if system := strings.Join(systemParts, "\n\n"); system != "" && len(turns) > 0 && turns[0].prefix == "Human: " {
		turns[0].text = system + "\n\n" + turns[0].text
	}

	var transcript strings.Builder
	for i, t := range turns {
		if i > 0 {
			transcript.WriteString("\n\n")
		}
		transcript.WriteString(t.prefix)
		transcript.WriteString(t.text)
	}

	req := claudeAICompletionRequest{
		Attachments:        []any{},
		Files:              []string{},
		Locale:             "en-US",
		Model:              model,
		PersonalizedStyles: []map[string]any{claudeAIDefaultStyle()},
		Prompt:             transcript.String() + "\n\nAssistant: ",
		RenderingMode:      "messages",
		SyncSources:        []string{},
		Timezone:           "UTC",
		Tools:              []map[string]any{},
		TurnMessageUUIDs: map[string]string{
			"human_message_uuid":     randomUUID(),
			"assistant_message_uuid": randomUUID(),
		},
		CreateConversation: map[string]any{
			"name":                             "",
			"model":                            model,
			"include_conversation_preferences": true,
			"is_temporary":                     true,
			"enabled_imagine":                  true,
		},
		ParentMessageUUID: nilMessageUUID,
	}

	out, err := json.Marshal(req)
	return out, in.Stream, err
}

// messageText extracts plain text from an OpenAI message content value, which
// may be a plain string or an array of content parts.
func messageText(content json.RawMessage) string {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &parts) == nil {
		var sb strings.Builder
		for _, p := range parts {
			if p.Type == "text" && p.Text != "" {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(p.Text)
			}
		}
		return sb.String()
	}
	return ""
}

// aggregateClaudeAIStream reads claude.ai's completion SSE stream and folds it
// into a single Anthropic Messages response JSON document.
func aggregateClaudeAIStream(src io.ReadCloser) ([]byte, error) {
	defer src.Close()

	var (
		text       strings.Builder
		msgID      string
		stopReason string
		inputTok   int64
		outputTok  int64
	)

	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
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
		case "message_start":
			var evt struct {
				Message struct {
					ID string `json:"id"`
				} `json:"message"`
			}
			if json.Unmarshal([]byte(data), &evt) == nil && evt.Message.ID != "" {
				msgID = evt.Message.ID
			}
		case "content_block_delta":
			var evt struct {
				Delta struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"delta"`
			}
			if json.Unmarshal([]byte(data), &evt) == nil && evt.Delta.Text != "" {
				text.WriteString(evt.Delta.Text)
			}
		case "message_delta":
			var evt struct {
				Delta struct {
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
				Usage struct {
					InputTokens  int64 `json:"input_tokens"`
					OutputTokens int64 `json:"output_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal([]byte(data), &evt) == nil {
				if evt.Delta.StopReason != "" {
					stopReason = evt.Delta.StopReason
				}
				if evt.Usage.InputTokens > 0 {
					inputTok = evt.Usage.InputTokens
				}
				if evt.Usage.OutputTokens > 0 {
					outputTok = evt.Usage.OutputTokens
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	msg := map[string]any{
		"id":      msgID,
		"type":    "message",
		"role":    "assistant",
		"content": []map[string]any{{"type": "text", "text": text.String()}},
		"usage": map[string]any{
			"input_tokens":  inputTok,
			"output_tokens": outputTok,
		},
	}
	if stopReason != "" {
		msg["stop_reason"] = stopReason
	}
	return json.Marshal(msg)
}
