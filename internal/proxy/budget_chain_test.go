package proxy_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Rethinger/2papi/internal/config"
	"github.com/Rethinger/2papi/internal/proxy"
	"github.com/Rethinger/2papi/internal/resilience"
	"github.com/Rethinger/2papi/internal/router"
	"github.com/Rethinger/2papi/internal/server"
)

func usageServer(body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
}

func policyApp(t *testing.T, keys []config.VirtualKey, models []config.Model, upstreams []*httptest.Server) *httptest.Server {
	t.Helper()
	accounts := make([]config.Account, 0, len(upstreams))
	for i, up := range upstreams {
		accounts = append(accounts, config.Account{Name: fmt.Sprintf("acct-%d", i), BaseURL: up.URL, APIKey: "ak", Enabled: true, Priority: i + 1, Weight: 1, MaxConcurrency: 10})
	}
	snap, err := config.Build(config.Config{Version: 1, Secret: "s", VirtualKeys: keys, Models: models, Accounts: accounts, Routing: config.Routing{Strategy: "priority", MaxAttempts: 2}, Resilience: config.Resilience{CircuitFailures: 10}})
	if err != nil {
		t.Fatal(err)
	}
	st := resilience.New()
	rt := router.New(snap, st)
	px := proxy.New(snap, st, rt)
	ts := httptest.NewServer(server.New(snap, px).Routes())
	t.Cleanup(ts.Close)
	return ts
}

func postBody(t *testing.T, ts *httptest.Server, key, body string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func TestBudgetExceededReturns429AndHeaders(t *testing.T) {
	up := usageServer(`{"model":"up","usage":{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110}}`)
	defer up.Close()
	// input $6/Mtok, output $12/Mtok → 100/1e6*6 + 10/1e6*12 = $0.00072 per request.
	ts := policyApp(t,
		[]config.VirtualKey{{Name: "vk", Key: "sk", Models: []string{"m"}, RPM: 100, BudgetUSD: 0.0007}},
		[]config.Model{{Alias: "m", UpstreamModel: "up", Accounts: []string{"acct-0"}, InputCostPerMtok: 6, OutputCostPerMtok: 12}},
		[]*httptest.Server{up},
	)

	resp, body := postBody(t, ts, "sk", `{"model":"m","stream":false,"messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("first request status=%d body=%s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("X-Gateway-Budget-Remaining"); !strings.HasPrefix(got, "0.0007") {
		t.Fatalf("budget header=%q", got)
	}

	resp, body = postBody(t, ts, "sk", `{"model":"m","stream":false,"messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 429 {
		t.Fatalf("overspent request status=%d body=%s headers=%v", resp.StatusCode, body, resp.Header)
	}
	if !strings.Contains(body, "budget_exceeded") {
		t.Fatalf("expected budget_exceeded in body: %s", body)
	}
}

func TestTPMAndConcurrencyHeaders(t *testing.T) {
	up := usageServer(`{"model":"up","usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`)
	defer up.Close()
	ts := policyApp(t,
		[]config.VirtualKey{{Name: "vk", Key: "sk", Models: []string{"m"}, RPM: 10, TPM: 500, MaxConcurrency: 3}},
		[]config.Model{{Alias: "m", UpstreamModel: "up", Accounts: []string{"acct-0"}}},
		[]*httptest.Server{up},
	)
	resp, body := postBody(t, ts, "sk", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("X-Gateway-RateLimit-Remaining"); got != "9" {
		t.Fatalf("rpm header=%q", got)
	}
	if got := resp.Header.Get("X-Gateway-TPM-Remaining"); got != "500" {
		t.Fatalf("tpm header=%q", got)
	}
	if got := resp.Header.Get("X-Gateway-Concurrency-Remaining"); got != "2" {
		t.Fatalf("concurrency header=%q", got)
	}
}

func TestFallbackChainMovesToNextAlias(t *testing.T) {
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", 500)
	}))
	defer broken.Close()
	ok := usageServer(`{"model":"up2","usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`)
	defer ok.Close()
	ts := policyApp(t,
		[]config.VirtualKey{{Name: "vk", Key: "sk", Models: []string{"m"}, RPM: 100}},
		[]config.Model{
			{Alias: "m", UpstreamModel: "up", Accounts: []string{"acct-0"}, Fallbacks: []string{"m2"}},
			{Alias: "m2", UpstreamModel: "up2", Accounts: []string{"acct-1"}},
		},
		[]*httptest.Server{broken, ok},
	)

	resp, body := postBody(t, ts, "sk", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	if resp.Header.Get("X-Gateway-Route") != "acct-1" {
		t.Fatalf("route=%q want acct-1", resp.Header.Get("X-Gateway-Route"))
	}
}

func TestFallbackChainExhaustedReturns502(t *testing.T) {
	broken1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", 500)
	}))
	defer broken1.Close()
	broken2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", 503)
	}))
	defer broken2.Close()
	ts := policyApp(t,
		[]config.VirtualKey{{Name: "vk", Key: "sk", Models: []string{"m"}, RPM: 100}},
		[]config.Model{
			{Alias: "m", UpstreamModel: "up", Accounts: []string{"acct-0"}, Fallbacks: []string{"m2"}},
			{Alias: "m2", UpstreamModel: "up2", Accounts: []string{"acct-1"}},
		},
		[]*httptest.Server{broken1, broken2},
	)
	resp, body := postBody(t, ts, "sk", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 502 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
}
