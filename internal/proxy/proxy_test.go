package proxy_test

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Rethinger/2papi/internal/adapter"
	"github.com/Rethinger/2papi/internal/cache"
	"github.com/Rethinger/2papi/internal/config"
	"github.com/Rethinger/2papi/internal/protocol"
	"github.com/Rethinger/2papi/internal/proxy"
	"github.com/Rethinger/2papi/internal/resilience"
	"github.com/Rethinger/2papi/internal/router"
	"github.com/Rethinger/2papi/internal/server"
	"github.com/Rethinger/2papi/internal/telemetry"
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

func TestResponseModelRewritePreservesLargeJSONNumberLexeme(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"1","model":"up","usage":{"total_tokens":900719925474099312345},"score":1.2300}`)
	}))
	defer up.Close()
	ts := oneUpstreamApp(t, up.URL)
	defer ts.Close()
	resp, body := post(ts, "sk", `{"model":"m","user":"response-number"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	for _, want := range []string{`"model":"m"`, `"total_tokens":900719925474099312345`, `"score":1.2300`} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %s: %s", want, body)
		}
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
	for _, h := range []string{"X-Leak", "Connection", "Keep-Alive", "Proxy-Connection"} {
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
	req.Header.Set("Proxy-Connection", "keep-alive")
	req.Header.Set("X-Remove", "secret")
	req.Header.Set("TE", "trailers")
	req.Header.Set("X-Gateway-Route", "spoofed")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	h := <-seen
	for _, name := range []string{"Connection", "Proxy-Connection", "X-Remove", "TE", "X-Gateway-Route"} {
		if got := h.Get(name); got != "" {
			t.Fatalf("%s forwarded as %q", name, got)
		}
	}
}

func TestParseRetryAfterAcceptsZeroAndRejectsOverflow(t *testing.T) {
	def := 5 * time.Second
	if got := proxy.ParseRetryAfter("0", def); got != 0 {
		t.Fatalf("zero retry-after = %s", got)
	}
	if got := proxy.ParseRetryAfter("9223372036854775807", def); got != def {
		t.Fatalf("overflow retry-after = %s", got)
	}
}

func TestQuotaCooldownUsesCodexResetHeaderAndIsBounded(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	h := http.Header{}
	h.Set("X-Codex-Primary-Reset-At", fmt.Sprint(now.Add(90*time.Second).Unix()))
	if got := proxy.ParseQuotaCooldown(h, 5*time.Second, now); got != 90*time.Second {
		t.Fatalf("reset cooldown=%s", got)
	}
	h.Set("X-Codex-Primary-Reset-At", fmt.Sprint(now.Add(30*24*time.Hour).Unix()))
	if got := proxy.ParseQuotaCooldown(h, 5*time.Second, now); got != 7*24*time.Hour {
		t.Fatalf("unbounded cooldown=%s", got)
	}
	h.Set("Retry-After", "12")
	if got := proxy.ParseQuotaCooldown(h, 5*time.Second, now); got != 12*time.Second {
		t.Fatalf("retry-after priority=%s", got)
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

func TestSharedTransportTuned(t *testing.T) {
	snap := proxyTestSnapshot(t, []string{"a"})
	st := resilience.New()
	p := proxy.New(snap, st, router.New(snap, st))
	pt, ok := p.Client.Transport.(*proxy.PoolTransport)
	if !ok {
		t.Fatalf("transport type %T, want *proxy.PoolTransport", p.Client.Transport)
	}
	tr := pt.Direct()
	if tr.MaxIdleConnsPerHost != 128 {
		t.Fatalf("MaxIdleConnsPerHost=%d want 128", tr.MaxIdleConnsPerHost)
	}
	if !tr.ForceAttemptHTTP2 {
		t.Fatalf("ForceAttemptHTTP2=false want true")
	}
	if tr.IdleConnTimeout != 90*time.Second {
		t.Fatalf("IdleConnTimeout=%s want 90s", tr.IdleConnTimeout)
	}
	if p.Client.Timeout != 0 {
		t.Fatalf("client timeout=%s want 0", p.Client.Timeout)
	}
}

type pipeCaptureRecorder struct {
	mu    sync.Mutex
	event telemetry.Event
	got   bool
}

func (r *pipeCaptureRecorder) Record(e telemetry.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.event = e
	r.got = true
}

func TestPipePassthroughNonStreaming(t *testing.T) {
	upstreamBody := `{"id":"1","model":"m","usage":{"prompt_tokens":3,"completion_tokens":7,"total_tokens":10}}`
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(upstreamBody)))
		_, _ = io.WriteString(w, upstreamBody)
	}))
	defer up.Close()
	snap, err := config.Build(config.Config{Version: 1, Secret: "s", VirtualKeys: []config.VirtualKey{{Name: "vk", Key: "sk", Models: []string{"m"}, RPM: 10}}, Models: []config.Model{{Alias: "m", UpstreamModel: "m", Accounts: []string{"a"}}}, Accounts: []config.Account{{Name: "a", BaseURL: up.URL, APIKey: "a", Enabled: true, Weight: 1, MaxConcurrency: 10}}, Resilience: config.Resilience{CircuitFailures: 1}})
	if err != nil {
		t.Fatal(err)
	}
	st := resilience.New()
	rt := router.New(snap, st)
	px := proxy.New(snap, st, rt)
	rec := &pipeCaptureRecorder{}
	px.Telemetry = rec
	ts := httptest.NewServer(server.New(snap, px).Routes())
	defer ts.Close()
	reqBody := `{"model":"m"}`
	req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer sk")
	req.Header.Set("X-Gateway-Cache", "true")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, got)
	}
	if string(got) != upstreamBody {
		t.Fatalf("pipe body mismatch: got=%s", got)
	}
	key := px.Cache.KeyFor("m", []byte(reqBody))
	var entry cache.Entry
	hit := false
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if entry, hit = px.Cache.Get(key); hit {
			break
		}
	}
	if !hit {
		t.Fatal("cache not written on pipe path")
	}
	if string(entry.Body) != upstreamBody {
		t.Fatalf("cache body=%s", entry.Body)
	}
	ev := waitEvent(t, rec)
	if ev.InputTokens != 3 || ev.OutputTokens != 7 || ev.TotalTokens != 10 {
		t.Fatalf("usage input=%d output=%d total=%d", ev.InputTokens, ev.OutputTokens, ev.TotalTokens)
	}
}

func TestPipeTruncatedSkipsCacheAndUsage(t *testing.T) {
	big := `{"id":"1","model":"m","padding":"` + strings.Repeat("x", 16<<20) + `"}`
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(big)))
		_, _ = io.WriteString(w, big)
	}))
	defer up.Close()
	snap, err := config.Build(config.Config{Version: 1, Secret: "s", VirtualKeys: []config.VirtualKey{{Name: "vk", Key: "sk", Models: []string{"m"}, RPM: 10}}, Models: []config.Model{{Alias: "m", UpstreamModel: "m", Accounts: []string{"a"}}}, Accounts: []config.Account{{Name: "a", BaseURL: up.URL, APIKey: "a", Enabled: true, Weight: 1, MaxConcurrency: 10}}, Resilience: config.Resilience{CircuitFailures: 1}})
	if err != nil {
		t.Fatal(err)
	}
	st := resilience.New()
	rt := router.New(snap, st)
	px := proxy.New(snap, st, rt)
	rec := &pipeCaptureRecorder{}
	px.Telemetry = rec
	ts := httptest.NewServer(server.New(snap, px).Routes())
	defer ts.Close()
	reqBody := `{"model":"m"}`
	req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer sk")
	req.Header.Set("X-Gateway-Cache", "true")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if string(got) != big {
		t.Fatalf("truncated pipe must still deliver full body: got %d bytes want %d", len(got), len(big))
	}
	key := px.Cache.KeyFor("m", []byte(reqBody))
	if _, hit := px.Cache.Get(key); hit {
		t.Fatal("cache must not be written for truncated pipe")
	}
	ev := waitEvent(t, rec)
	if ev.TotalTokens != 0 || ev.InputTokens != 0 || ev.OutputTokens != 0 {
		t.Fatalf("usage must stay zero on truncation: input=%d output=%d total=%d", ev.InputTokens, ev.OutputTokens, ev.TotalTokens)
	}
}

func waitEvent(t *testing.T, rec *pipeCaptureRecorder) telemetry.Event {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		rec.mu.Lock()
		got := rec.got
		ev := rec.event
		rec.mu.Unlock()
		if got {
			return ev
		}
		if time.Now().After(deadline) {
			t.Fatal("telemetry event not recorded")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestLatencyHeaders(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: hi\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer up.Close()
	ts := oneUpstreamApp(t, up.URL)
	defer ts.Close()
	resp, body := post(ts, "sk", `{"model":"m","stream":true}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	overhead, err := strconv.ParseInt(resp.Header.Get("X-Gateway-Overhead-MS"), 10, 64)
	if err != nil {
		t.Fatalf("X-Gateway-Overhead-MS missing/invalid: %v", err)
	}
	upstream, err := strconv.ParseInt(resp.Header.Get("X-Gateway-Upstream-MS"), 10, 64)
	if err != nil {
		t.Fatalf("X-Gateway-Upstream-MS missing/invalid: %v", err)
	}
	if overhead < 0 || upstream < 0 {
		t.Fatalf("negative latency headers overhead=%d upstream=%d", overhead, upstream)
	}
}

func gzipProxyApp(t *testing.T, gzipEnabled bool) (*httptest.Server, *proxy.Proxy, string) {
	padding := strings.Repeat("x", 2048)
	upstreamBody := `{"id":"1","model":"up","padding":"` + padding + `"}`
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(upstreamBody)))
		_, _ = io.WriteString(w, upstreamBody)
	}))
	t.Cleanup(up.Close)
	snap, err := config.Build(config.Config{Version: 1, Secret: "s", Server: config.Server{Gzip: gzipEnabled}, VirtualKeys: []config.VirtualKey{{Name: "vk", Key: "sk", Models: []string{"m"}, RPM: 10}}, Models: []config.Model{{Alias: "m", UpstreamModel: "up", Accounts: []string{"a"}}}, Accounts: []config.Account{{Name: "a", BaseURL: up.URL, APIKey: "a", Enabled: true, Weight: 1, MaxConcurrency: 10}}, Resilience: config.Resilience{CircuitFailures: 1}})
	if err != nil {
		t.Fatal(err)
	}
	st := resilience.New()
	rt := router.New(snap, st)
	px := proxy.New(snap, st, rt)
	ts := httptest.NewServer(server.New(snap, px).Routes())
	t.Cleanup(ts.Close)
	return ts, px, strings.Replace(upstreamBody, `"model":"up"`, `"model":"m"`, 1)
}

func gunzipAll(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	gr, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gr.Close()
	b, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("gunzip read: %v", err)
	}
	return b
}

func TestGzipResponse(t *testing.T) {
	ts, _, want := gzipProxyApp(t, true)
	req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Authorization", "Bearer sk")
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding=%q want gzip", resp.Header.Get("Content-Encoding"))
	}
	if resp.Header.Get("Content-Length") != "" {
		t.Fatalf("Content-Length=%q must be removed", resp.Header.Get("Content-Length"))
	}
	if resp.Header.Get("Vary") != "Accept-Encoding" {
		t.Fatalf("Vary=%q want Accept-Encoding", resp.Header.Get("Vary"))
	}
	got := gunzipAll(t, resp)
	resp.Body.Close()
	if string(got) != want {
		t.Fatalf("gunzipped body mismatch:\n got=%s\nwant=%s", got, want)
	}
	// Without Accept-Encoding the same path stays plain.
	req2, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	req2.Header.Set("Authorization", "Bearer sk")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.Header.Get("Content-Encoding") != "" {
		t.Fatalf("Content-Encoding=%q want empty without Accept-Encoding", resp2.Header.Get("Content-Encoding"))
	}
	if string(plain) != want {
		t.Fatalf("plain body mismatch: %s", plain)
	}
}

func TestGzipCacheHit(t *testing.T) {
	ts, _, want := gzipProxyApp(t, true)
	body := `{"model":"m"}`
	req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk")
	req.Header.Set("X-Gateway-Cache", "true")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.Header.Get("X-Gateway-Cache") != "MISS" {
		t.Fatalf("first request cache=%q want MISS", resp.Header.Get("X-Gateway-Cache"))
	}
	req2, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", strings.NewReader(body))
	req2.Header.Set("Authorization", "Bearer sk")
	req2.Header.Set("X-Gateway-Cache", "true")
	req2.Header.Set("Accept-Encoding", "gzip")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	if resp2.Header.Get("X-Gateway-Cache") != "HIT" {
		t.Fatalf("second request cache=%q want HIT", resp2.Header.Get("X-Gateway-Cache"))
	}
	if resp2.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("cache-hit Content-Encoding=%q want gzip", resp2.Header.Get("Content-Encoding"))
	}
	if got := resp2.Header.Get("Content-Length"); got == strconv.Itoa(len(want)) {
		t.Fatalf("cache-hit Content-Length=%q must not be the stale uncompressed length", got)
	}
	got := gunzipAll(t, resp2)
	resp2.Body.Close()
	if string(got) != want {
		t.Fatalf("cache-hit gunzipped body mismatch: %s", got)
	}
}

func TestGzipDisabled(t *testing.T) {
	ts, _, want := gzipProxyApp(t, false)
	req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Authorization", "Bearer sk")
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.Header.Get("Content-Encoding") != "" {
		t.Fatalf("Content-Encoding=%q want empty when gzip disabled", resp.Header.Get("Content-Encoding"))
	}
	if string(plain) != want {
		t.Fatalf("body mismatch: %s", plain)
	}
}
