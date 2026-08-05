package proxy_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/1jehuang/2papi/internal/adapter"
	"github.com/1jehuang/2papi/internal/config"
	"github.com/1jehuang/2papi/internal/protocol"
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

type errAdapter struct{ err error }

func (a errAdapter) Execute(context.Context, adapter.Execution) (*adapter.Result, error) {
	return nil, a.err
}
func (a errAdapter) Operate(context.Context, adapter.Operation) (adapter.OperationResult, error) {
	return adapter.OperationResult{}, nil
}

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
func TestResponseModelAliasRewrite(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"1","model":"up","choices":[]}`)
	}))
	defer up.Close()
	snap, err := config.Build(config.Config{Version: 1, Secret: "s", VirtualKeys: []config.VirtualKey{{Name: "vk", Key: "sk", Models: []string{"m"}, RPM: 10}}, Models: []config.Model{{Alias: "m", UpstreamModel: "up", Accounts: []string{"a"}}}, Accounts: []config.Account{{Name: "a", BaseURL: up.URL, APIKey: "a", Enabled: true, Weight: 1, MaxConcurrency: 10}}, Resilience: config.Resilience{CircuitFailures: 1}})
	if err != nil {
		t.Fatal(err)
	}
	st := resilience.New()
	rt := router.New(snap, st)
	ts := httptest.NewServer(server.New(snap, proxy.New(snap, st, rt)).Routes())
	defer ts.Close()
	resp, body := post(ts, "sk", `{"model":"m"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, `"model":"m"`) || strings.Contains(body, `"model":"up"`) {
		t.Fatalf("body=%s", body)
	}
}

func TestRetryAfterCooldownFallback(t *testing.T) {
	up1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		http.Error(w, "limited", http.StatusTooManyRequests)
	}))
	up2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"ok":1}`) }))
	defer up1.Close()
	defer up2.Close()
	snap, err := config.Build(config.Config{Version: 1, Secret: "s", VirtualKeys: []config.VirtualKey{{Name: "vk", Key: "sk", Models: []string{"m"}, RPM: 10}}, Models: []config.Model{{Alias: "m", UpstreamModel: "up", Accounts: []string{"a", "b"}}}, Accounts: []config.Account{{Name: "a", BaseURL: up1.URL, APIKey: "a", Enabled: true, Priority: 1, Weight: 1, MaxConcurrency: 10}, {Name: "b", BaseURL: up2.URL, APIKey: "b", Enabled: true, Priority: 2, Weight: 1, MaxConcurrency: 10}}, Routing: config.Routing{Strategy: "priority", MaxAttempts: 2}, Resilience: config.Resilience{CircuitFailures: 1}})
	if err != nil {
		t.Fatal(err)
	}
	st := resilience.New()
	rt := router.New(snap, st)
	ts := httptest.NewServer(server.New(snap, proxy.New(snap, st, rt)).Routes())
	defer ts.Close()
	resp, body := post(ts, "sk", `{"model":"m"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	resp, _ = post(ts, "sk", `{"model":"m","user":"retry-after-new-affinity"}`)
	if resp.Header.Get("X-Gateway-Route") != "b" {
		t.Fatalf("route=%s", resp.Header.Get("X-Gateway-Route"))
	}
}

func TestSpoofedGatewayAndHopByHopResponseHeadersFiltered(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Gateway-Route", "spoofed")
		w.Header().Set("X-Gateway-Attempts", "99")
		w.Header().Set("Connection", "X-Leak")
		w.Header().Set("X-Leak", "secret")
		w.Header().Set("Keep-Alive", "timeout=5")
		fmt.Fprint(w, `{"ok":1}`)
	}))
	defer up.Close()
	ts := oneUpstreamApp(t, up.URL)
	defer ts.Close()
	resp, _ := post(ts, "sk", `{"model":"m","user":"headers"}`)
	if resp.Header.Get("X-Gateway-Route") != "a" || resp.Header.Get("X-Gateway-Attempts") != "1" {
		t.Fatalf("gateway headers route=%q attempts=%q", resp.Header.Get("X-Gateway-Route"), resp.Header.Get("X-Gateway-Attempts"))
	}
	for _, h := range []string{"X-Leak", "Connection", "Keep-Alive"} {
		if got := resp.Header.Get(h); got != "" {
			t.Fatalf("%s leaked as %q", h, got)
		}
	}
}

func TestHopByHopRequestHeadersFiltered(t *testing.T) {
	seen := make(chan http.Header, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Clone()
		fmt.Fprint(w, `{"ok":1}`)
	}))
	defer up.Close()
	ts := oneUpstreamApp(t, up.URL)
	defer ts.Close()
	req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m","user":"request-headers"}`))
	req.Header.Set("Authorization", "Bearer sk")
	req.Header.Set("Connection", "X-Remove")
	req.Header.Set("X-Remove", "secret")
	req.Header.Set("TE", "trailers")
	req.Header.Set("X-Gateway-Route", "spoofed")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	h := <-seen
	for _, name := range []string{"Connection", "X-Remove", "TE", "X-Gateway-Route"} {
		if got := h.Get(name); got != "" {
			t.Fatalf("%s forwarded as %q", name, got)
		}
	}
}

func TestCanceledContextDoesNotRetryOrWriteSynthetic502(t *testing.T) {
	snap, err := config.Build(config.Config{Version: 1, Secret: "s", VirtualKeys: []config.VirtualKey{{Name: "vk", Key: "sk", Models: []string{"m"}, RPM: 10}}, Models: []config.Model{{Alias: "m", UpstreamModel: "up", Accounts: []string{"a", "b"}}}, Accounts: []config.Account{{Name: "a", BaseURL: "http://a", APIKey: "a", Enabled: true, Priority: 1, Weight: 1, MaxConcurrency: 10}, {Name: "b", BaseURL: "http://b", APIKey: "b", Enabled: true, Priority: 2, Weight: 1, MaxConcurrency: 10}}, Routing: config.Routing{Strategy: "priority", MaxAttempts: 2}, Resilience: config.Resilience{CircuitFailures: 1}})
	if err != nil {
		t.Fatal(err)
	}
	reg := adapter.NewRegistry()
	_ = reg.Register("openai-compatible", errAdapter{err: context.Canceled})
	st := resilience.New()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	rec := httptest.NewRecorder()
	proxy.NewWithRegistry(snap, st, router.New(snap, st), reg).Chat(rec, req, protocolMeta("m"), []byte(`{"model":"m"}`))
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("unexpected synthetic response code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestWhitespaceJSONTopLevelModelRewrite(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{ "id" : "1", "model" : "up", "nested":{"model":"up"}}`)
	}))
	defer up.Close()
	ts := oneUpstreamApp(t, up.URL)
	defer ts.Close()
	resp, body := post(ts, "sk", `{"model":"m","user":"rewrite"}`)
	if resp.StatusCode != 200 || !strings.Contains(body, `"model":"m"`) || !strings.Contains(body, `"nested":{"model":"up"}`) {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
}

func TestInvalidJSONRewriteFailsClosedAndRetries(t *testing.T) {
	var calls atomic.Int32
	up1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, `{`) }))
	up2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, `{"ok":1}`) }))
	defer up1.Close()
	defer up2.Close()
	ts := twoUpstreamApp(t, up1.URL, up2.URL)
	defer ts.Close()
	resp, body := post(ts, "sk", `{"model":"m","user":"invalid-json"}`)
	if resp.StatusCode != 200 || calls.Load() != 2 || !strings.Contains(body, `"ok":1`) {
		t.Fatalf("status=%d calls=%d body=%s", resp.StatusCode, calls.Load(), body)
	}
}

func TestReadFailureRewriteRetries(t *testing.T) {
	reg := adapter.NewRegistry()
	_ = reg.Register("openai-compatible", &sequenceAdapter{results: []adapterResult{{body: errReader{}}, {body: strings.NewReader(`{"ok":1}`)}}})
	snap := proxyTestSnapshot(t, []string{"a", "b"})
	st := resilience.New()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	rec := httptest.NewRecorder()
	proxy.NewWithRegistry(snap, st, router.New(snap, st), reg).Chat(rec, req, protocolMeta("m"), []byte(`{"model":"m"}`))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ok":1`) {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestOversizeRewriteFailsClosedAndRetries(t *testing.T) {
	reg := adapter.NewRegistry()
	_ = reg.Register("openai-compatible", &sequenceAdapter{results: []adapterResult{{body: io.LimitReader(zeroReader{}, 16<<20+1)}, {body: strings.NewReader(`{"ok":1}`)}}})
	snap := proxyTestSnapshot(t, []string{"a", "b"})
	st := resilience.New()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	rec := httptest.NewRecorder()
	proxy.NewWithRegistry(snap, st, router.New(snap, st), reg).Chat(rec, req, protocolMeta("m"), []byte(`{"model":"m"}`))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ok":1`) {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestNoRetryAfterFirstStreamingByteVisible(t *testing.T) {
	var calls atomic.Int32
	up1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: first\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	up2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, `{"ok":1}`) }))
	defer up1.Close()
	defer up2.Close()
	ts := twoUpstreamApp(t, up1.URL, up2.URL)
	defer ts.Close()
	resp, body := post(ts, "sk", `{"model":"m","stream":true,"user":"stream-no-retry"}`)
	if resp.StatusCode != 200 || calls.Load() != 1 || !strings.Contains(body, "data: first") {
		t.Fatalf("status=%d calls=%d body=%s", resp.StatusCode, calls.Load(), body)
	}
}

func TestBaseURLPathPreservedWhenJoiningOpenAIEndpoint(t *testing.T) {
	seen := make(chan string, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.URL.EscapedPath()
		fmt.Fprint(w, `{"ok":1}`)
	}))
	defer up.Close()
	ts := oneUpstreamApp(t, up.URL+"/tenant/base/")
	defer ts.Close()
	resp, body := post(ts, "sk", `{"model":"m","user":"base-path"}`)
	if resp.StatusCode != 200 || !strings.Contains(body, `"ok":1`) {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	if got := <-seen; got != "/tenant/base/v1/chat/completions" {
		t.Fatalf("joined path=%q", got)
	}
}

func TestBaseURLEscapedPathPreservedWhenJoiningOpenAIEndpoint(t *testing.T) {
	seen := make(chan string, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.URL.EscapedPath()
		fmt.Fprint(w, `{"ok":1}`)
	}))
	defer up.Close()
	ts := oneUpstreamApp(t, up.URL+"/tenant%2Fsafe/base")
	defer ts.Close()
	resp, body := post(ts, "sk", `{"model":"m","user":"escaped-base-path"}`)
	if resp.StatusCode != 200 || !strings.Contains(body, `"ok":1`) {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	if got := <-seen; got != "/tenant%2Fsafe/base/v1/chat/completions" {
		t.Fatalf("joined escaped path=%q", got)
	}
}

func TestBaseURLQueryOrFragmentRejectedBeforeUpstream(t *testing.T) {
	for _, suffix := range []string{"?token=bad", "#frag"} {
		t.Run(suffix, func(t *testing.T) {
			var calls atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				fmt.Fprint(w, `{"ok":1}`)
			}))
			defer up.Close()
			ts := oneUpstreamApp(t, up.URL+"/base"+suffix)
			defer ts.Close()
			resp, _ := post(ts, "sk", `{"model":"m","user":"bad-base-url"}`)
			if resp.StatusCode != 502 {
				t.Fatalf("status=%d", resp.StatusCode)
			}
			if calls.Load() != 0 {
				t.Fatalf("upstream called %d times", calls.Load())
			}
		})
	}
}

func oneUpstreamApp(t *testing.T, url string) *httptest.Server { return twoUpstreamApp(t, url, url) }

func twoUpstreamApp(t *testing.T, a, b string) *httptest.Server {
	snap, err := config.Build(config.Config{Version: 1, Secret: "s", VirtualKeys: []config.VirtualKey{{Name: "vk", Key: "sk", Models: []string{"m"}, RPM: 10}}, Models: []config.Model{{Alias: "m", UpstreamModel: "up", Accounts: []string{"a", "b"}}}, Accounts: []config.Account{{Name: "a", BaseURL: a, APIKey: "a", Enabled: true, Priority: 1, Weight: 1, MaxConcurrency: 10}, {Name: "b", BaseURL: b, APIKey: "b", Enabled: true, Priority: 2, Weight: 1, MaxConcurrency: 10}}, Routing: config.Routing{Strategy: "priority", MaxAttempts: 2}, Resilience: config.Resilience{CircuitFailures: 1}})
	if err != nil {
		t.Fatal(err)
	}
	st := resilience.New()
	return httptest.NewServer(server.New(snap, proxy.New(snap, st, router.New(snap, st))).Routes())
}

func proxyTestSnapshot(t *testing.T, accounts []string) *config.Snapshot {
	var cfgAccounts []config.Account
	for _, name := range accounts {
		cfgAccounts = append(cfgAccounts, config.Account{Name: name, BaseURL: "http://" + name, APIKey: name, Enabled: true, Weight: 1, MaxConcurrency: 10})
	}
	snap, err := config.Build(config.Config{Version: 1, Secret: "s", VirtualKeys: []config.VirtualKey{{Name: "vk", Key: "sk", Models: []string{"m"}, RPM: 10}}, Models: []config.Model{{Alias: "m", UpstreamModel: "up", Accounts: accounts}}, Accounts: cfgAccounts, Resilience: config.Resilience{CircuitFailures: 1}})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func protocolMeta(model string) protocol.ChatMetadata { return protocol.ChatMetadata{Model: model} }

type adapterResult struct{ body io.Reader }
type sequenceAdapter struct {
	results []adapterResult
	calls   atomic.Int32
}

func (a *sequenceAdapter) Execute(context.Context, adapter.Execution) (*adapter.Result, error) {
	i := int(a.calls.Add(1)) - 1
	return &adapter.Result{Status: 200, Header: http.Header{}, Body: io.NopCloser(a.results[i].body)}, nil
}
func (a *sequenceAdapter) Operate(context.Context, adapter.Operation) (adapter.OperationResult, error) {
	return adapter.OperationResult{}, nil
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = ' '
	}
	return len(p), nil
}
