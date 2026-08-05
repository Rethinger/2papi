package openai_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/1jehuang/2papi/internal/adapter"
	adapteropenai "github.com/1jehuang/2papi/internal/adapter/openai"
	"github.com/1jehuang/2papi/internal/config"
)

func TestExecuteRewritesRequestModelAndAuthorization(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"model":"upstream","ok":true}`)
	}))
	defer up.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"public"}`))
	req.Header.Set("Authorization", "Bearer client")
	res, err := adapteropenai.New(up.Client()).Execute(context.Background(), adapter.Execution{Endpoint: adapter.EndpointChatCompletions, Request: req, Account: account(up.URL), Model: config.Model{Alias: "public", UpstreamModel: "upstream"}, PublicModel: "public", Body: []byte(`{"model":"public"}`)})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if gotPath != adapteropenai.ChatCompletionsPath {
		t.Fatalf("path=%s", gotPath)
	}
	if gotAuth != "Bearer upstream-key" {
		t.Fatalf("auth=%q", gotAuth)
	}
	if !strings.Contains(gotBody, `"model":"upstream"`) {
		t.Fatalf("body=%s", gotBody)
	}
}

func TestExecuteStreamsUncommittedResult(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: one\n\ndata: [DONE]\n\n")
	}))
	defer up.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"public"}`))
	res, err := adapteropenai.New(up.Client()).Execute(context.Background(), adapter.Execution{Endpoint: adapter.EndpointChatCompletions, Request: req, Account: account(up.URL), Model: config.Model{Alias: "public", UpstreamModel: "upstream"}, PublicModel: "public", Body: []byte(`{"model":"public"}`)})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.Status != http.StatusOK || res.Header.Get("Content-Type") != "text/event-stream" || !strings.Contains(string(body), "[DONE]") {
		t.Fatalf("status=%d content-type=%q body=%q", res.Status, res.Header.Get("Content-Type"), string(body))
	}
}

func TestOperateDiscoverModelsAndCapabilityError(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != adapteropenai.ModelsPath {
			t.Fatalf("path=%s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer upstream-key" {
			t.Fatalf("auth=%q", r.Header.Get("Authorization"))
		}
		_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
	}))
	defer up.Close()
	ad := adapteropenai.New(up.Client())
	out, err := ad.Operate(context.Background(), adapter.Operation{Kind: adapter.OperationDiscoverModels, Account: account(up.URL)})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out.Data, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, err := ad.Operate(context.Background(), adapter.Operation{Kind: adapter.OperationReadUsage, Account: account(up.URL)}); err == nil {
		t.Fatal("expected capability error")
	}
}

func account(base string) config.Account {
	return config.Account{Name: "a", Adapter: adapteropenai.Name, BaseURL: base, Credential: config.Credential{Kind: "api_key", APIKey: "upstream-key", Revision: 1}}
}
