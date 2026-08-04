package proxy_test

import (
	"fmt"
	"github.com/1jehuang/2papi/internal/config"
	"github.com/1jehuang/2papi/internal/proxy"
	"github.com/1jehuang/2papi/internal/resilience"
	"github.com/1jehuang/2papi/internal/router"
	"github.com/1jehuang/2papi/internal/server"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func app(t *testing.T, firstStatus int) (*httptest.Server, *atomic.Int32) {
	var calls atomic.Int32
	up1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if firstStatus > 0 {
			http.Error(w, "fail", firstStatus)
			return
		}
		fmt.Fprint(w, `{"ok":1}`)
	}))
	up2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: one\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(up1.Close)
	t.Cleanup(up2.Close)
	snap, err := config.Build(config.Config{Version: 1, Secret: "s", VirtualKeys: []config.VirtualKey{{Name: "vk", Key: "sk", Models: []string{"m"}, RPM: 2}}, Models: []config.Model{{Alias: "m", UpstreamModel: "up", Accounts: []string{"a", "b"}}}, Accounts: []config.Account{{Name: "a", BaseURL: up1.URL, APIKey: "a", Enabled: true, Priority: 1, Weight: 1, MaxConcurrency: 10}, {Name: "b", BaseURL: up2.URL, APIKey: "b", Enabled: true, Priority: 2, Weight: 1, MaxConcurrency: 10}}, Routing: config.Routing{Strategy: "priority", MaxAttempts: 2}, Resilience: config.Resilience{CircuitFailures: 1}})
	if err != nil {
		t.Fatal(err)
	}
	st := resilience.New()
	rt := router.New(snap, st)
	px := proxy.New(snap, st, rt)
	return httptest.NewServer(server.New(snap, px).Routes()), &calls
}
func post(ts *httptest.Server, key, body string) (*http.Response, string) {
	req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, _ := http.DefaultClient.Do(req)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}
func TestAuthRateLimitAndModels(t *testing.T) {
	ts, _ := app(t, 0)
	defer ts.Close()
	r, _ := http.Get(ts.URL + "/v1/models")
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	resp, _ := post(ts, "bad", `{"model":"m"}`)
	if resp.StatusCode != 401 {
		t.Fatal(resp.StatusCode)
	}
	resp, _ = post(ts, "sk", `{"model":"m"}`)
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	post(ts, "sk", `{"model":"m"}`)
	resp, _ = post(ts, "sk", `{"model":"m"}`)
	if resp.StatusCode != 429 {
		t.Fatalf("want rate limit got %d", resp.StatusCode)
	}
}
func TestFallbackBeforeStreamingAndHeaders(t *testing.T) {
	ts, calls := app(t, 500)
	defer ts.Close()
	resp, body := post(ts, "sk", `{"model":"m","stream":true,"user":"u"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("X-Gateway-Route") != "b" {
		t.Fatal(resp.Header.Get("X-Gateway-Route"))
	}
	if resp.Header.Get("X-Gateway-Attempts") != "2" {
		t.Fatal(resp.Header.Get("X-Gateway-Attempts"))
	}
	if calls.Load() != 2 {
		t.Fatal(calls.Load())
	}
	if !strings.Contains(body, "[DONE]") {
		t.Fatal(body)
	}
}
