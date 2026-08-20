package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Rethinger/2papi/internal/adapter"
	"github.com/Rethinger/2papi/internal/config"
)

func TestResponsesNonStreamRewritesAndHeaders(t *testing.T) {
	var upstreamPath, auth, acctID, client string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamPath = r.URL.Path
		auth = r.Header.Get("Authorization")
		acctID = r.Header.Get("ChatGPT-Account-ID")
		client = r.Header.Get("X-Codex-Client")
		if r.Header.Get("X-Gateway-Secret") != "" {
			t.Fatalf("gateway header leaked")
		}
		if r.Header.Get("X-Request-Leak") != "" {
			t.Fatalf("Connection-named request header leaked")
		}
		for _, h := range []string{"Cookie", "Set-Cookie", "X-Api-Key", "X-Secret-Token"} {
			if r.Header.Get(h) != "" {
				t.Fatalf("credential-like request header %s leaked", h)
			}
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["model"] != "upstream-model" {
			t.Fatalf("model not rewritten upstream: %#v", body["model"])
		}
		w.Header().Set("Set-Cookie", "secret=1")
		w.Header().Set("X-Safe", "ok")
		_, _ = io.WriteString(w, `{"id":"r1","model":"upstream-model","codex.rate_limits":{"remaining":3}}`)
	}))
	defer up.Close()

	ad := New(up.Client(), nil, nil, Options{TestMode: true, BackendBaseURL: up.URL, ClientVersion: "test-client"})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"public-model","input":"hi","stream":false}`))
	req.Header.Set("X-Gateway-Secret", "drop")
	req.Header.Set("Connection", "X-Request-Leak")
	req.Header.Set("X-Request-Leak", "drop")
	req.Header.Set("Cookie", "session=client")
	req.Header.Set("Set-Cookie", "client=bad")
	req.Header.Set("X-Api-Key", "client-key")
	req.Header.Set("X-Secret-Token", "client-secret")
	res, err := ad.Execute(req.Context(), adapter.Execution{Endpoint: adapter.EndpointResponses, Request: req, Account: codexAccount(), Model: config.Model{Alias: "public-model", UpstreamModel: "upstream-model"}, PublicModel: "public-model", Body: []byte(`{"model":"public-model","input":"hi","stream":false}`)})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if upstreamPath != responsesPath {
		t.Fatalf("path = %s", upstreamPath)
	}
	if auth != "Bearer access" || acctID != "acct" || client != "test-client" {
		t.Fatalf("bad codex headers auth=%q acct=%q client=%q", auth, acctID, client)
	}
	if res.Header.Get("Set-Cookie") != "" || res.Header.Get("X-Safe") != "ok" {
		t.Fatalf("bad response headers: %#v", res.Header)
	}
	b, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(b), `"model":"public-model"`) || !strings.Contains(string(b), `"codex.rate_limits"`) {
		t.Fatalf("bad body %s", b)
	}
}

func TestResponsesRequestRewriteRejectsTrailingJSON(t *testing.T) {
	for _, body := range [][]byte{[]byte(`{"model":"public"} junk`), []byte(`{"model":"public"}{"model":"second"}`)} {
		if _, err := rewriteResponsesRequestModel(body, "upstream"); err == nil {
			t.Fatalf("accepted trailing tokens in %s", body)
		}
	}
}

func TestResponsesRequestRewriteConvertsStringInputForCodexBackend(t *testing.T) {
	out, err := rewriteResponsesRequestModel([]byte(`{"model":"public","input":"hello"}`), "upstream")
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Model string `json:"model"`
		Store bool   `json:"store"`
		Input []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Model != "upstream" || payload.Store || len(payload.Input) != 1 || payload.Input[0].Type != "message" || payload.Input[0].Role != "user" || len(payload.Input[0].Content) != 1 || payload.Input[0].Content[0].Type != "input_text" || payload.Input[0].Content[0].Text != "hello" {
		t.Fatalf("unexpected payload: %s", out)
	}
}

func TestResponsesRequestRewriteForcesStoreFalse(t *testing.T) {
	out, err := rewriteResponsesRequestModel([]byte(`{"model":"public","input":[],"store":true}`), "upstream")
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Store bool `json:"store"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Store {
		t.Fatalf("store was not forced false: %s", out)
	}
}

func TestCollectResponsesSSEForNonStreamingClient(t *testing.T) {
	stream := strings.NewReader("data: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\"}}\n\n" +
		"data: {\"type\":\"codex.rate_limits\",\"plan_type\":\"plus\"}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"model\":\"upstream\",\"output\":[]}}\n\n" +
		"data: [DONE]\n\n")
	out, rateLimits, err := collectResponsesSSE(stream, "upstream", "public", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := json.Unmarshal(out, &response); err != nil {
		t.Fatal(err)
	}
	output, ok := response["output"].([]any)
	if response["id"] != "r1" || response["model"] != "public" || !ok || len(output) != 1 || !strings.Contains(string(out), "OK") || !strings.Contains(string(rateLimits), "plan_type") {
		t.Fatalf("response=%s rate_limits=%s", out, rateLimits)
	}
}

func TestCollectResponsesSSERewritesVersionedCanonicalModel(t *testing.T) {
	stream := strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"model\":\"gpt-5.4-mini-2026-03-17\",\"output\":[]}}\n\n")
	out, _, err := collectResponsesSSE(stream, "gpt-5.4-mini", "public-mini", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := json.Unmarshal(out, &response); err != nil {
		t.Fatal(err)
	}
	if response["model"] != "public-mini" {
		t.Fatalf("model not rewritten: %s", out)
	}
}

func TestResponsesRewritePreservesTrailingResponseBytes(t *testing.T) {
	in := []byte(`{"model":"upstream"} trailing`)
	out, observed, err := rewriteJSONModelAndRateLimits(bytes.NewReader(in), "upstream", "public", 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, in) || len(observed) != 0 {
		t.Fatalf("response trailing bytes not preserved out=%s observed=%s", out, observed)
	}
}

func TestResponsesObservesRateLimitsAndDropsConnectionHeaders(t *testing.T) {
	var observed atomic.Int32
	var observedHeader http.Header
	ad := New(nil, nil, nil, Options{TestMode: true, CodexRateLimitObserver: func(ctx context.Context, o CodexRateLimitObservation) {
		observed.Add(1)
		if len(o.Header) > 0 {
			observedHeader = o.Header.Clone()
		}
		if o.Account != "codex" || o.PublicModel != "public-model" {
			t.Fatalf("bad observation scope: %#v", o)
		}
		if len(o.JSON) > 0 && !strings.Contains(string(o.JSON), "plan_type") {
			t.Fatalf("bad json observation: %s", o.JSON)
		}
	}})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Connection", "X-Transient")
		w.Header().Set("X-Transient", "drop")
		w.Header().Set("X-Codex-Chat-Primary-Used-Percent", "42")
		w.Header().Set("Set-Cookie", "secret=1")
		w.Header().Set("X-Secret", "nope")
		_, _ = io.WriteString(w, `{"model":"upstream-model","codex.rate_limits":{"plan_type":"plus","rate_limits":{"primary":{"used_percent":42},"secondary":{"used_percent":7}},"credits":{"has_credits":true},"metered_limit_name":"chat"}}`)
	}))
	defer up.Close()
	ad.options.BackendBaseURL = up.URL

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"public-model"}`))
	res, err := ad.Execute(req.Context(), adapter.Execution{Endpoint: adapter.EndpointResponses, Request: req, Account: codexAccount(), Model: config.Model{Alias: "public-model", UpstreamModel: "upstream-model"}, PublicModel: "public-model", Body: []byte(`{"model":"public-model"}`)})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	_, _ = io.ReadAll(res.Body)
	if res.Header.Get("X-Transient") != "" {
		t.Fatalf("Connection-named response header leaked: %#v", res.Header)
	}
	if observed.Load() < 2 {
		t.Fatalf("expected header and json rate-limit observations, got %d", observed.Load())
	}
	if observedHeader.Get("X-Codex-Chat-Primary-Used-Percent") != "42" {
		t.Fatalf("rate-limit header not observed: %#v", observedHeader)
	}
	if observedHeader.Get("Set-Cookie") != "" || observedHeader.Get("X-Secret") != "" {
		t.Fatalf("unsafe observer header leaked: %#v", observedHeader)
	}
}

func TestResponsesRetriesOnceOnUnauthorizedWithRefreshedToken(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case responsesPath:
			call := calls.Add(1)
			if call == 1 {
				if r.Header.Get("Authorization") != "Bearer stale" {
					t.Fatalf("first token = %q", r.Header.Get("Authorization"))
				}
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if call == 2 && r.Header.Get("Authorization") != "Bearer fresh" {
				t.Fatalf("retry token = %q", r.Header.Get("Authorization"))
			}
			_, _ = io.WriteString(w, `{"model":"upstream-model"}`)
		case "/oauth/token":
			_, _ = io.WriteString(w, `{"access_token":"fresh","expires_in":3600}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	ad := New(up.Client(), nil, nil, Options{TestMode: true, AuthBaseURL: up.URL, BackendBaseURL: up.URL})
	acct := codexAccount()
	acct.Credential.AccessToken = "stale"
	acct.Credential.RefreshToken = "refresh"
	acct.Credential.ClientID = "client"
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"public-model"}`))
	res, err := ad.Execute(req.Context(), adapter.Execution{Endpoint: adapter.EndpointResponses, Request: req, Account: acct, Model: config.Model{Alias: "public-model", UpstreamModel: "upstream-model"}, PublicModel: "public-model", Body: []byte(`{"model":"public-model"}`)})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if calls.Load() != 2 || res.Status != http.StatusOK {
		t.Fatalf("calls=%d status=%d", calls.Load(), res.Status)
	}
}

func TestResponsesDropsOversizedRateLimitObservationJSON(t *testing.T) {
	var observed atomic.Int32
	ad := New(nil, nil, nil, Options{TestMode: true, CodexRateLimitObserver: func(ctx context.Context, o CodexRateLimitObservation) {
		observed.Add(1)
	}})
	ad.observeRateLimits(context.Background(), adapter.Execution{Account: codexAccount(), PublicModel: "public-model"}, nil, json.RawMessage(`{"type":"codex.rate_limits","padding":"`+strings.Repeat("x", maxRateLimitObservationBytes)+`"}`))
	if observed.Load() != 0 {
		t.Fatalf("oversized rate-limit JSON should be dropped, observed %d", observed.Load())
	}
}

func TestResponsesSSERewritesModel(t *testing.T) {
	var observed atomic.Int32
	var observedJSON json.RawMessage
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data:{\"type\":\"response.created\",\"response\":{\"id\":\"r1\",\"model\":\"upstream-model\"}}\n\ndata: {\"type\":\"codex.rate_limits\",\"plan_type\":\"plus\",\"rate_limits\":{\"primary\":{\"used_percent\":42},\"secondary\":{\"used_percent\":7}},\"credits\":{\"has_credits\":true},\"metered_limit_name\":\"chat\"}\n\ndata: [DONE]\n\n")
	}))
	defer up.Close()
	ad := New(up.Client(), nil, nil, Options{TestMode: true, BackendBaseURL: up.URL, CodexRateLimitObserver: func(ctx context.Context, o CodexRateLimitObservation) {
		if len(o.JSON) > 0 {
			observed.Add(1)
			observedJSON = append(json.RawMessage(nil), o.JSON...)
		}
	}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"public-model","stream":true}`))
	res, err := ad.Execute(req.Context(), adapter.Execution{Endpoint: adapter.EndpointResponses, Request: req, Account: codexAccount(), Model: config.Model{Alias: "public-model", UpstreamModel: "upstream-model"}, PublicModel: "public-model", Body: []byte(`{"model":"public-model","stream":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if strings.Contains(string(b), "upstream-model") || !strings.Contains(string(b), "public-model") || !strings.Contains(string(b), "[DONE]") {
		t.Fatalf("bad sse %s", b)
	}
	if observed.Load() != 1 || !strings.Contains(string(observedJSON), `"type":"codex.rate_limits"`) || !strings.Contains(string(observedJSON), `"plan_type":"plus"`) {
		t.Fatalf("bad SSE rate-limit observation count=%d json=%s", observed.Load(), observedJSON)
	}
}

func codexAccount() config.Account {
	return config.Account{Name: "codex", ID: "codex", Adapter: Name, Enabled: true, Credential: config.Credential{Kind: "codex", AccessToken: "access", ChatGPTAccountID: "acct", Revision: 1}}
}
