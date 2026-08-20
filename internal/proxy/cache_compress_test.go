package proxy_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Rethinger/2papi/internal/config"
	"github.com/Rethinger/2papi/internal/proxy"
	"github.com/Rethinger/2papi/internal/resilience"
	"github.com/Rethinger/2papi/internal/router"
	"github.com/Rethinger/2papi/internal/server"
)

func TestResponseCacheHitAndMiss(t *testing.T) {
	var upstreamCalls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chat-1","object":"chat.completion","model":"up","choices":[{"index":0,"message":{"role":"assistant","content":"hello"}}]}`)
	}))
	defer up.Close()

	snap, err := config.Build(config.Config{
		Version: 1,
		Secret:  "s",
		VirtualKeys: []config.VirtualKey{
			{Name: "vk", Key: "sk", Models: []string{"m"}, RPM: 100},
		},
		Models: []config.Model{
			{Alias: "m", UpstreamModel: "up", Accounts: []string{"acct-0"}},
		},
		Accounts: []config.Account{
			{Name: "acct-0", BaseURL: up.URL, APIKey: "k", Enabled: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	st := resilience.New()
	rt := router.New(snap, st)
	px := proxy.New(snap, st, rt)
	ts := httptest.NewServer(server.New(snap, px).Routes())
	defer ts.Close()

	reqBody := `{"model":"m","stream":false,"messages":[{"role":"user","content":"cached prompt"}]}`

	// 1. First request with X-Gateway-Cache: true -> MISS, upstream called
	req1, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", strings.NewReader(reqBody))
	req1.Header.Set("Authorization", "Bearer sk")
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("X-Gateway-Cache", "true")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp1.Body.Close()

	if resp1.StatusCode != 200 {
		t.Fatalf("first request status=%d", resp1.StatusCode)
	}
	if got := resp1.Header.Get("X-Gateway-Cache"); got != "MISS" {
		t.Fatalf("expected X-Gateway-Cache: MISS, got %q", got)
	}
	if upstreamCalls.Load() != 1 {
		t.Fatalf("expected 1 upstream call, got %d", upstreamCalls.Load())
	}

	// 2. Second request with same body -> HIT, upstream NOT called
	req2, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", strings.NewReader(reqBody))
	req2.Header.Set("Authorization", "Bearer sk")
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-Gateway-Cache", "true")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != 200 {
		t.Fatalf("second request status=%d", resp2.StatusCode)
	}
	if got := resp2.Header.Get("X-Gateway-Cache"); got != "HIT" {
		t.Fatalf("expected X-Gateway-Cache: HIT, got %q", got)
	}
	if upstreamCalls.Load() != 1 {
		t.Fatalf("upstream should NOT be called on cache hit, got %d calls", upstreamCalls.Load())
	}
}

func TestToolResultCompression(t *testing.T) {
	var receivedBody []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"c","object":"chat.completion","model":"up","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer up.Close()

	snap, err := config.Build(config.Config{
		Version: 1,
		Secret:  "s",
		VirtualKeys: []config.VirtualKey{
			{Name: "vk", Key: "sk", Models: []string{"m"}, RPM: 100},
		},
		Models: []config.Model{
			{Alias: "m", UpstreamModel: "up", Accounts: []string{"acct-0"}},
		},
		Accounts: []config.Account{
			{Name: "acct-0", BaseURL: up.URL, APIKey: "k", Enabled: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	st := resilience.New()
	rt := router.New(snap, st)
	px := proxy.New(snap, st, rt)
	ts := httptest.NewServer(server.New(snap, px).Routes())
	defer ts.Close()

	var lines []string
	for i := 0; i < 100; i++ {
		lines = append(lines, fmt.Sprintf("line %d: some lengthy output text here", i))
	}
	largeOutput := strings.Join(lines, "\\n")
	rawJSON := fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":"run"},{"role":"tool","content":"%s","tool_call_id":"c1"}]}`, largeOutput)

	req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", strings.NewReader(rawJSON))
	req.Header.Set("Authorization", "Bearer sk")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gateway-Compress", "true")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Gateway-Saved-Bytes"); got == "" || got == "0" {
		t.Fatalf("expected saved bytes header, got %q", got)
	}
	if !strings.Contains(string(receivedBody), "elided by gateway compression") {
		t.Fatalf("upstream should have received compressed payload: %s", string(receivedBody))
	}
}

func TestToolResultCompressionFromSnapshotFlagWithoutHeader(t *testing.T) {
	var receivedBody []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"c","object":"chat.completion","model":"up","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer up.Close()

	snap, err := config.Build(config.Config{
		Version: 1,
		Secret:  "s",
		VirtualKeys: []config.VirtualKey{
			{Name: "vk", Key: "sk", Models: []string{"m"}, RPM: 100},
		},
		Models: []config.Model{
			{Alias: "m", UpstreamModel: "up", Accounts: []string{"acct-0"}},
		},
		Accounts: []config.Account{
			{Name: "acct-0", BaseURL: up.URL, APIKey: "k", Enabled: true},
		},
		Optimization: config.Optimization{RTKCompression: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	st := resilience.New()
	rt := router.New(snap, st)
	px := proxy.New(snap, st, rt)
	ts := httptest.NewServer(server.New(snap, px).Routes())
	defer ts.Close()

	var lines []string
	for i := 0; i < 100; i++ {
		lines = append(lines, fmt.Sprintf("line %d: some lengthy output text here", i))
	}
	largeOutput := strings.Join(lines, "\\n")
	rawJSON := fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":"run"},{"role":"tool","content":"%s","tool_call_id":"c1"}]}`, largeOutput)

	// No X-Gateway-Compress header: compression must come from the snapshot flag.
	req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", strings.NewReader(rawJSON))
	req.Header.Set("Authorization", "Bearer sk")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Gateway-Saved-Bytes"); got == "" || got == "0" {
		t.Fatalf("expected saved bytes header from snapshot flag, got %q", got)
	}
	if !strings.Contains(string(receivedBody), "elided by gateway compression") {
		t.Fatalf("upstream should have received compressed payload: %s", string(receivedBody))
	}
}

func TestCavemanDirectiveFromSnapshotFlag(t *testing.T) {
	var receivedBody []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"c","object":"chat.completion","model":"up","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer up.Close()

	snap, err := config.Build(config.Config{
		Version: 1,
		Secret:  "s",
		VirtualKeys: []config.VirtualKey{
			{Name: "vk", Key: "sk", Models: []string{"m"}, RPM: 100},
		},
		Models: []config.Model{
			{Alias: "m", UpstreamModel: "up", Accounts: []string{"acct-0"}},
		},
		Accounts: []config.Account{
			{Name: "acct-0", BaseURL: up.URL, APIKey: "k", Enabled: true},
		},
		Optimization: config.Optimization{Caveman: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	st := resilience.New()
	rt := router.New(snap, st)
	px := proxy.New(snap, st, rt)
	ts := httptest.NewServer(server.New(snap, px).Routes())
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer sk")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if resp.Header.Get("X-Gateway-Caveman") != "true" {
		t.Fatalf("expected X-Gateway-Caveman header, got %q", resp.Header.Get("X-Gateway-Caveman"))
	}
	if !strings.Contains(string(receivedBody), "smart caveman") {
		t.Fatalf("upstream should have received the caveman directive: %s", string(receivedBody))
	}
}
