package proxy

import (
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rethinger/2papi/internal/proxylib"
)

func netListen() (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }

func base64Std(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// recordingProxy is a minimal HTTP proxy that records absolute-URI requests.
type recordingProxy struct {
	mu       sync.Mutex
	requests []*http.Request
	srv      *httptest.Server
}

func newRecordingProxy(t *testing.T) *recordingProxy {
	t.Helper()
	rp := &recordingProxy{}
	rp.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rp.mu.Lock()
		rp.requests = append(rp.requests, r.Clone(r.Context()))
		rp.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(rp.srv.Close)
	return rp
}

func (rp *recordingProxy) count() int {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	return len(rp.requests)
}

func (rp *recordingProxy) first() *http.Request {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	if len(rp.requests) == 0 {
		return nil
	}
	return rp.requests[0]
}

func proxyURL(t *testing.T, srv *httptest.Server, withCreds bool) string {
	t.Helper()
	addr := strings.TrimPrefix(srv.URL, "http://")
	if withCreds {
		return "http://proxyuser:proxypass@" + addr
	}
	return "http://" + addr
}

func testGroup(t *testing.T, entries ...string) *proxyGroup {
	t.Helper()
	var parsed []proxylib.Entry
	for _, raw := range entries {
		e, err := proxylib.ParseEntry(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		parsed = append(parsed, e)
	}
	return BuildGroup(parsed)
}

func testPoolTransport(t *testing.T, entries ...string) *poolTransport {
	t.Helper()
	pt := &poolTransport{direct: newSharedTransport()}
	if len(entries) > 0 {
		pt.SetGlobal(testGroup(t, entries...))
	}
	return pt
}

func doVia(t *testing.T, pt *poolTransport, ctx context.Context) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://upstream.example/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	return pt.RoundTrip(req)
}

func TestPoolTransportDirectWithoutGroup(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("direct"))
	}))
	defer upstream.Close()

	pt := &poolTransport{direct: newSharedTransport()}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, upstream.URL, nil)
	resp, err := pt.RoundTrip(req)
	if err != nil {
		t.Fatalf("direct roundtrip: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "direct" {
		t.Fatalf("body %q", body)
	}
}

func TestPoolTransportRoutesThroughProxy(t *testing.T) {
	rp := newRecordingProxy(t)
	pt := testPoolTransport(t, proxyURL(t, rp.srv, false))

	resp, err := doVia(t, pt, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if rp.count() != 1 {
		t.Fatalf("proxy saw %d requests, want 1", rp.count())
	}
	req := rp.first()
	if req == nil || req.Host != "upstream.example" {
		t.Fatalf("proxy saw %v, want upstream.example", req)
	}
}

func TestPoolTransportSendsProxyAuth(t *testing.T) {
	rp := newRecordingProxy(t)
	pt := testPoolTransport(t, proxyURL(t, rp.srv, true))
	resp, err := doVia(t, pt, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	req := rp.first()
	if req == nil {
		t.Fatal("proxy saw no request")
	}
	auth := req.Header.Get("Proxy-Authorization")
	want := "Basic " + base64Std("proxyuser:proxypass")
	if auth != want {
		t.Fatalf("Proxy-Authorization = %q, want %q", auth, want)
	}
}

func TestPoolTransportRoundRobin(t *testing.T) {
	rp1 := newRecordingProxy(t)
	rp2 := newRecordingProxy(t)
	pt := testPoolTransport(t,
		proxyURL(t, rp1.srv, false),
		proxyURL(t, rp2.srv, false),
	)
	for i := 0; i < 4; i++ {
		resp, err := doVia(t, pt, context.Background())
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	if rp1.count() != 2 || rp2.count() != 2 {
		t.Fatalf("round-robin: proxy1=%d proxy2=%d, want 2/2", rp1.count(), rp2.count())
	}
}

func TestPoolTransportFailoverOnDialError(t *testing.T) {
	// Dead first proxy: a listener that accepts and closes immediately still
	// produces a successful dial; use a closed listener for a hard dial error.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadAddr := strings.TrimPrefix(dead.URL, "http://")
	dead.Close() // port is now closed → dial error

	alive := newRecordingProxy(t)
	pt := testPoolTransport(t,
		"http://"+deadAddr,
		proxyURL(t, alive.srv, false),
	)
	resp, err := doVia(t, pt, context.Background())
	if err != nil {
		t.Fatalf("expected failover to second proxy: %v", err)
	}
	defer resp.Body.Close()
	if alive.count() != 1 {
		t.Fatalf("alive proxy saw %d requests, want 1", alive.count())
	}
}

func TestPoolTransportNoRetryOnNonDialError(t *testing.T) {
	// A proxy that returns an HTTP 500 is a successful roundtrip (no
	// retry): the response must surface as-is.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer bad.Close()
	alive := newRecordingProxy(t)
	pt := testPoolTransport(t,
		proxyURL(t, bad, false),
		proxyURL(t, alive.srv, false),
	)
	resp, err := doVia(t, pt, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 500 {
		t.Fatalf("status %d, want 500 from first proxy (no failover on HTTP errors)", resp.StatusCode)
	}
	if alive.count() != 0 {
		t.Fatalf("second proxy should not be tried on HTTP error, saw %d", alive.count())
	}
}

func TestPoolTransportBypass(t *testing.T) {
	rp := newRecordingProxy(t)
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer direct.Close()

	pt := testPoolTransport(t, proxyURL(t, rp.srv, false))
	ctx := BypassPool(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, direct.URL, nil)
	resp, err := pt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if rp.count() != 0 {
		t.Fatalf("bypassed request went through proxy, saw %d", rp.count())
	}
}

func TestPoolTransportRecordsMaskedUse(t *testing.T) {
	rp := newRecordingProxy(t)
	pt := testPoolTransport(t, proxyURL(t, rp.srv, true))
	ctx, use := WithProxyUse(context.Background())
	ctx = InjectGroup(ctx, testGroup(t, proxyURL(t, rp.srv, true)))
	resp, err := pt.RoundTrip(mustReq(ctx, "http://upstream.example/v1/chat/completions"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	used := use.Used()
	if used == "" {
		t.Fatal("proxy use not recorded")
	}
	if strings.Contains(used, "proxypass") {
		t.Fatalf("used proxy leaked password: %q", used)
	}
	if !strings.Contains(used, "proxyuser:****") {
		t.Fatalf("used proxy not masked: %q", used)
	}
}

func TestPoolTransportGroupFromContextBeatsGlobal(t *testing.T) {
	globalRP := newRecordingProxy(t)
	accountRP := newRecordingProxy(t)
	pt := testPoolTransport(t, proxyURL(t, globalRP.srv, false))
	ctx := InjectGroup(context.Background(), testGroup(t, proxyURL(t, accountRP.srv, false)))
	resp, err := pt.RoundTrip(mustReq(ctx, "http://upstream.example/v1/models"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if accountRP.count() != 1 {
		t.Fatalf("account group not used, saw %d requests", accountRP.count())
	}
	if globalRP.count() != 0 {
		t.Fatalf("global pool used despite account group, saw %d", globalRP.count())
	}
}

func mustReq(ctx context.Context, url string) *http.Request {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		panic(err)
	}
	return req
}

func TestPoolTransportFailoverAcrossGroupThenGlobal(t *testing.T) {
	// Account group with a dead proxy, global pool with an alive one:
	// after the account group is exhausted, the transport must NOT fall to
	// the global pool (group is authoritative).
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadAddr := strings.TrimPrefix(dead.URL, "http://")
	dead.Close()

	alive := newRecordingProxy(t)
	pt := testPoolTransport(t, proxyURL(t, alive.srv, false))
	ctx := InjectGroup(context.Background(), testGroup(t, "http://"+deadAddr))
	resp, err := pt.RoundTrip(mustReq(ctx, "http://upstream.example/v1/models"))
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected error: account group dead, global pool must not be used")
	}
	if alive.count() != 0 {
		t.Fatalf("global pool used despite dead account group, saw %d", alive.count())
	}
}

func TestPoolTransportSetGlobalSwap(t *testing.T) {
	// Simulates snapshot adoption: the shared client/transport stays, the
	// global pool is swapped in/out atomically.
	rp := newRecordingProxy(t)
	pt := &poolTransport{direct: newSharedTransport()} // no global pool initially

	// No pool → direct (would fail to resolve upstream.example, so use a
	// real direct target).
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("direct"))
	}))
	defer direct.Close()
	resp, err := pt.RoundTrip(mustReq(context.Background(), direct.URL))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if rp.count() != 0 {
		t.Fatalf("no pool should mean direct, proxy saw %d", rp.count())
	}

	// Pool swapped in → routed through the proxy.
	pt.SetGlobal(testGroup(t, proxyURL(t, rp.srv, false)))
	resp, err = pt.RoundTrip(mustReq(context.Background(), "http://upstream.example/v1/models"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if rp.count() != 1 {
		t.Fatalf("after SetGlobal proxy saw %d requests, want 1", rp.count())
	}

	// Pool cleared → back to direct.
	pt.SetGlobal(nil)
	resp, err = pt.RoundTrip(mustReq(context.Background(), direct.URL))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if rp.count() != 1 {
		t.Fatalf("after SetGlobal(nil) proxy saw %d requests, want still 1", rp.count())
	}
}

func TestPoolTransportTimeoutProtection(t *testing.T) {
	// A hanging proxy must not stall forever: dial timeout applies.
	ln, err := listenClosed(t)
	if err != nil {
		t.Fatal(err)
	}
	pt := testPoolTransport(t, "http://"+ln)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	_, err = pt.RoundTrip(mustReq(ctx, "http://upstream.example/v1/models"))
	if err == nil {
		t.Fatal("expected error from dead proxy")
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("request took %s, transport hung", time.Since(start))
	}
}

func listenClosed(t *testing.T) (string, error) {
	t.Helper()
	ln, err := netListen()
	if err != nil {
		return "", err
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr, nil
}
