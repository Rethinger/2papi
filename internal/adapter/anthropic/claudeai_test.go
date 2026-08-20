package anthropic

import (
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

func TestTranslateOpenAIToClaudeAI(t *testing.T) {
	openAIBody := []byte(`{
  "model": "claude-public",
  "messages": [
    {"role": "system", "content": "You are a helpful assistant."},
    {"role": "user", "content": "Hi there"},
    {"role": "assistant", "content": "Hello!"},
    {"role": "user", "content": [{"type": "text", "text": "And again"}]},
    {"role": "tool", "tool_call_id": "call_1", "content": "line1\nline2\nline3"}
  ]
}`)

	payload, stream, err := translateOpenAIToClaudeAI(openAIBody, "claude-3-5-sonnet-20241022")
	if err != nil {
		t.Fatal(err)
	}
	if stream {
		t.Fatal("expected stream=false")
	}

	var req claudeAICompletionRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		t.Fatal(err)
	}
	if req.Model != "claude-3-5-sonnet-20241022" {
		t.Fatalf("model=%q", req.Model)
	}
	if req.ParentMessageUUID != nilMessageUUID {
		t.Fatalf("parent=%q", req.ParentMessageUUID)
	}
	if req.CreateConversation == nil {
		t.Fatal("create_conversation_params missing for new conversation")
	}

	want := "Human: You are a helpful assistant.\n\nHi there\n\nAssistant: Hello!\n\nHuman: And again\n\nHuman: [tool_result]\nline1\nline2\nline3\n\nAssistant: "
	if req.Prompt != want {
		t.Fatalf("prompt mismatch:\n got: %q\nwant: %q", req.Prompt, want)
	}
	if req.TurnMessageUUIDs["human_message_uuid"] == "" || req.TurnMessageUUIDs["assistant_message_uuid"] == "" {
		t.Fatal("turn_message_uuids missing")
	}
}

func TestExecuteClaudeAICookieStreaming(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("cookie") != "sessionKey=sk-ant-secret" {
			http.Error(w, "bad cookie", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("content-type") != "application/json" {
			http.Error(w, "bad content type", http.StatusBadRequest)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/organizations/org-123/chat_conversations/") || !strings.HasSuffix(r.URL.Path, "/completion") {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req claudeAICompletionRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		if !strings.HasPrefix(req.Prompt, "Human: ") || !strings.HasSuffix(req.Prompt, "Assistant: ") {
			http.Error(w, "bad transcript", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		events := []string{
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_ai_1\",\"role\":\"assistant\"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello from claude.ai\"}}\n\n",
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
		Endpoint: adapter.EndpointChatCompletions,
		Account: config.Account{
			BaseURL: ts.URL,
			Credential: config.Credential{
				Kind:           "cookie",
				Cookies:        "sessionKey=sk-ant-secret",
				OrganizationID: "org-123",
			},
		},
		Model:       config.Model{Alias: "claude-public", UpstreamModel: "claude-3-5-sonnet-20241022"},
		PublicModel: "claude-public",
		Body:        []byte(`{"model":"claude-public","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.Status != http.StatusOK {
		t.Fatalf("status=%d", res.Status)
	}
	b, _ := io.ReadAll(res.Body)
	chunks := string(b)
	if !strings.Contains(chunks, "Hello from claude.ai") || !strings.Contains(chunks, "[DONE]") {
		t.Fatalf("unexpected translation:\n%s", chunks)
	}
}

func TestExecuteClaudeAINonStreamingAggregates(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_ai_2\"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\" world\"}}\n\n",
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":5,\"output_tokens\":2}}\n\n",
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		}
		for _, e := range events {
			_, _ = io.WriteString(w, e)
		}
	}))
	defer ts.Close()

	ad := New(ts.Client())
	res, err := ad.Execute(context.Background(), adapter.Execution{
		Endpoint: adapter.EndpointChatCompletions,
		Account: config.Account{
			BaseURL: ts.URL,
			Credential: config.Credential{
				Kind:           "cookie",
				Cookies:        "sessionKey=sk-ant-secret",
				OrganizationID: "org-123",
			},
		},
		Model:       config.Model{Alias: "claude-public", UpstreamModel: "claude-3-5-sonnet-20241022"},
		PublicModel: "claude-public",
		Body:        []byte(`{"model":"claude-public","messages":[{"role":"user","content":"hi"}]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	var out struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("bad response: %v\n%s", err, b)
	}
	if out.Model != "claude-public" {
		t.Fatalf("model=%q", out.Model)
	}
	if len(out.Choices) != 1 || out.Choices[0].Message.Content != "Hello world" {
		t.Fatalf("content mismatch: %+v", out.Choices)
	}
	if out.Usage.TotalTokens != 7 {
		t.Fatalf("usage=%d", out.Usage.TotalTokens)
	}
}

func TestExecuteClaudeAIResolvesOrgWhenMissing(t *testing.T) {
	var orgCalls int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/organizations" {
			orgCalls++
			if r.Header.Get("cookie") != "sessionKey=sk-ant-secret" {
				http.Error(w, "bad cookie", http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"organizations":[{"uuid":"auto-org-1","name":"Default"}]}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer ts.Close()

	ad := New(ts.Client())
	res, err := ad.Execute(context.Background(), adapter.Execution{
		Endpoint: adapter.EndpointChatCompletions,
		Account: config.Account{
			BaseURL: ts.URL,
			Credential: config.Credential{
				Kind:    "cookie",
				Cookies: "sessionKey=sk-ant-secret",
			},
		},
		Model:       config.Model{Alias: "claude-public", UpstreamModel: "claude-3-5-sonnet-20241022"},
		PublicModel: "claude-public",
		Body:        []byte(`{"model":"claude-public","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if orgCalls != 1 {
		t.Fatalf("expected one org discovery call, got %d", orgCalls)
	}
	if !strings.Contains(res.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("expected SSE response, got content-type %q", res.Header.Get("Content-Type"))
	}
}

func TestExecuteOAuthUsesBearerHeaders(t *testing.T) {
	var gotAuth, gotDangerous, gotBeta, gotApp string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("authorization")
		gotDangerous = r.Header.Get("anthropic-dangerous-direct-browser-access")
		gotBeta = r.Header.Get("anthropic-beta")
		gotApp = r.Header.Get("x-app")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id": "msg_oauth_1",
			"type": "message",
			"role": "assistant",
			"content": [{"type": "text", "text": "oauth ok"}],
			"stop_reason": "end_turn",
			"usage": {"input_tokens": 1, "output_tokens": 1}
		}`)
	}))
	defer ts.Close()

	ad := New(ts.Client())
	res, err := ad.Execute(context.Background(), adapter.Execution{
		Endpoint: adapter.EndpointChatCompletions,
		Account: config.Account{
			BaseURL: ts.URL,
			Credential: config.Credential{
				Kind:        "oauth",
				AccessToken: "sk-ant-oauth-token",
			},
		},
		Model:       config.Model{Alias: "claude-public", UpstreamModel: "claude-3-5-sonnet-20241022"},
		PublicModel: "claude-public",
		Body:        []byte(`{"model":"claude-public","messages":[{"role":"user","content":"hi"}]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if gotAuth != "Bearer sk-ant-oauth-token" {
		t.Fatalf("authorization=%q", gotAuth)
	}
	if gotDangerous != "true" || !strings.Contains(gotBeta, oauthBeta) || !strings.Contains(gotBeta, "claude-code") || gotApp != "cli" {
		t.Fatalf("oauth headers missing: dangerous=%q beta=%q app=%q", gotDangerous, gotBeta, gotApp)
	}
}

func TestValidateClaudeAICookieCredentials(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/organizations" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("cookie") != "sessionKey=sk-ant-secret" {
			http.Error(w, "bad cookie", http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"organizations":[{"uuid":"org-1"}]}`)
	}))
	defer ts.Close()

	ad := New(ts.Client())
	_, err := ad.Operate(context.Background(), adapter.Operation{
		Kind: adapter.OperationValidateCredentials,
		Account: config.Account{
			BaseURL: ts.URL,
			Credential: config.Credential{
				Kind:    "cookie",
				Cookies: "sessionKey=sk-ant-secret",
			},
		},
	})
	if err != nil {
		t.Fatalf("expected valid cookies: %v", err)
	}
}

func TestValidateClaudeAICookieRejected(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer ts.Close()

	ad := New(ts.Client())
	_, err := ad.Operate(context.Background(), adapter.Operation{
		Kind: adapter.OperationValidateCredentials,
		Account: config.Account{
			BaseURL: ts.URL,
			Credential: config.Credential{
				Kind:    "cookie",
				Cookies: "sessionKey=expired",
			},
		},
	})
	if err == nil {
		t.Fatal("expected invalid cookie credentials to fail")
	}
}
