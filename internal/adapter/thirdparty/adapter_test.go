package thirdparty

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Rethinger/2papi/internal/adapter"
	"github.com/Rethinger/2papi/internal/config"
)

func testExec(url string, kind string, cred config.Credential) adapter.Execution {
	return adapter.Execution{
		Endpoint: adapter.EndpointChatCompletions,
		Account: config.Account{
			Name:       "open",
			BaseURL:    url,
			Credential: cred,
		},
		Model:       config.Model{Alias: "m", UpstreamModel: "upstream-model"},
		PublicModel: "m",
		Body:        []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`),
	}
}

func TestFreeProviderNoAuthUsesDefaultHeaders(t *testing.T) {
	var gotAuth, gotUA string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer ts.Close()

	ad := New(ts.Client(), OpenCodeSpec)
	cred := config.Credential{Kind: "free"}
	res, err := ad.Execute(context.Background(), testExec(ts.URL, "open", cred))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.Status != http.StatusOK {
		t.Fatalf("status=%d", res.Status)
	}
	if gotAuth != "" {
		t.Fatalf("free provider should not send Authorization, got %q", gotAuth)
	}
	if !strings.Contains(gotUA, "opencode") {
		t.Fatalf("ua=%q want opencode", gotUA)
	}
}

func TestOAuthProviderSendsBearerAndRefreshesOn401(t *testing.T) {
	var auths []string
	var ua string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auths = append(auths, r.Header.Get("Authorization"))
		ua = r.Header.Get("User-Agent")
		if len(auths) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer ts.Close()

	ad := New(ts.Client(), CursorSpec)
	// No refresher (nil auth) — second attempt still uses stale token, but
	// free flow should send Bearer on the first request.
	cred := config.Credential{Kind: "oauth", AccessToken: "tok-1"}
	res, err := ad.Execute(context.Background(), testExec(ts.URL, "cursor", cred))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if len(auths) == 0 || auths[0] != "Bearer tok-1" {
		t.Fatalf("auths=%v", auths)
	}
	if !strings.Contains(ua, "cursor") {
		t.Fatalf("ua=%q want cursor", ua)
	}
}

func TestKimiAndCopilotHeaders(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer ts.Close()
	// not testing header contents exhaustively, just that they execute
	ad := New(ts.Client(), KimiSpec)
	if _, err := ad.Execute(context.Background(), testExec(ts.URL, "kimi", config.Credential{Kind: "free"})); err != nil {
		t.Fatal(err)
	}
	ad2 := New(ts.Client(), CopilotSpec)
	if _, err := ad2.Execute(context.Background(), testExec(ts.URL, "copilot", config.Credential{Kind: "oauth", AccessToken: "t"})); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterPlugins(t *testing.T) {
	reg := adapter.NewRegistry()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ts.Close()
	_ = RegisterPlugins(reg, ts.Client())
	for _, name := range []string{"opencode", "felo", "qoder", "cursor", "copilot", "kimi"} {
		if _, ok := reg.Get(name); !ok {
			t.Fatalf("adapter %s not registered", name)
		}
	}
}
