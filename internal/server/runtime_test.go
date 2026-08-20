package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Rethinger/2papi/internal/adapter"
	"github.com/Rethinger/2papi/internal/config"
	"github.com/Rethinger/2papi/internal/proxy"
	"github.com/Rethinger/2papi/internal/resilience"
	"github.com/Rethinger/2papi/internal/router"
)

func testSnapshot(alias string) *config.Snapshot {
	s, err := config.Build(config.Config{Version: 1, Secret: "s", VirtualKeys: []config.VirtualKey{{Name: "vk", Key: "secret", Models: []string{alias}, RPM: 100000}}, Models: []config.Model{{Alias: alias, UpstreamModel: "u", Accounts: []string{"a"}}}, Accounts: []config.Account{{Name: "a", BaseURL: "http://upstream", APIKey: "ak", Enabled: true}}})
	if err != nil {
		panic(err)
	}
	return s
}

func TestAtomicRuntimeSwapModelsConcurrent(t *testing.T) {
	gw := NewRuntimeServer(testSnapshot("m0"), resilience.New())
	h := gw.Routes()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			gw.Adopt(testSnapshot("m" + string(rune('a'+(i%26)))))
		}(i)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("status=%d", rec.Code)
				return
			}
			var body struct {
				Data []map[string]any `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Errorf("bad json: %v", err)
				return
			}
			if len(body.Data) != 1 {
				t.Errorf("models len=%d", len(body.Data))
			}
		}()
	}
	wg.Wait()
}

func TestResponsesRouteReusesAuthAllowlistRPMAndRouting(t *testing.T) {
	snap, err := config.Build(config.Config{Version: 2, Secret: "s", VirtualKeys: []config.VirtualKey{
		{Name: "vk", Key: "secret", Models: []string{"public"}, RPM: 1},
		{Name: "limited", Key: "limited", Models: []string{"other"}, RPM: 10},
	}, Models: []config.Model{{Alias: "public", UpstreamModel: "up", Accounts: []string{"a"}}}, Accounts: []config.Account{{ID: "a", Name: "a", Adapter: "openai-compatible", BaseURL: "http://fake", Credential: config.Credential{Kind: "api_key", APIKey: "ak", Revision: 1}, Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	fake := &runtimeFakeAdapter{body: []byte(`{"model":"public"}`)}
	h := testServerWithAdapter(t, snap, fake).Routes()

	for name, tc := range map[string]struct {
		key  string
		want int
	}{"auth": {"", 401}, "allowlist": {"limited", 403}} {
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte(`{"model":"public"}`)))
		if tc.key != "" {
			req.Header.Set("Authorization", "Bearer "+tc.key)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("%s status=%d want=%d", name, rec.Code, tc.want)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte(`{"model":"public"}{"model":"second"}`)))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("trailing metadata json status=%d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte(`{"model":"public"}`)))
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || fake.endpoint.Load() != int32(len(adapter.EndpointResponses)) || fake.calls.Load() != 1 {
		t.Fatalf("routing status=%d endpointLen=%d calls=%d", rec.Code, fake.endpoint.Load(), fake.calls.Load())
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte(`{"model":"public"}`)))
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("rpm status=%d", rec.Code)
	}
}

func TestResponsesSSEDoesNotFallbackAfterFirstFlushedEvent(t *testing.T) {
	snap, err := config.Build(config.Config{Version: 2, Secret: "s", VirtualKeys: []config.VirtualKey{{Name: "vk", Key: "secret", Models: []string{"public"}, RPM: 100}}, Models: []config.Model{{Alias: "public", UpstreamModel: "up", Accounts: []string{"a", "b"}}}, Accounts: []config.Account{
		{ID: "a", Name: "a", Adapter: "openai-compatible", BaseURL: "http://fake", Credential: config.Credential{Kind: "api_key", APIKey: "ak", Revision: 1}, Enabled: true, Priority: 1},
		{ID: "b", Name: "b", Adapter: "openai-compatible", BaseURL: "http://fake", Credential: config.Credential{Kind: "api_key", APIKey: "ak", Revision: 1}, Enabled: true, Priority: 2},
	}})
	if err != nil {
		t.Fatal(err)
	}
	fake := &runtimeFakeAdapter{streamErr: true, body: []byte("data: {\"ok\":true}\n\n")}
	h := testServerWithAdapter(t, snap, fake).Routes()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte(`{"model":"public","stream":true}`)))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"ok":true`)) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if fake.calls.Load() != 1 {
		t.Fatalf("fallback attempted after flushed SSE event, calls=%d", fake.calls.Load())
	}
}

func testServerWithAdapter(t *testing.T, snap *config.Snapshot, fake adapter.Adapter) *Server {
	t.Helper()
	st := resilience.New()
	reg := adapter.NewRegistry()
	if err := reg.Register("openai-compatible", fake); err != nil {
		t.Fatal(err)
	}
	px := proxy.NewWithRegistry(snap, st, router.New(snap, st), reg)
	return New(snap, px)
}

type runtimeFakeAdapter struct {
	calls     atomic.Int32
	endpoint  atomic.Int32
	body      []byte
	streamErr bool
}

func (a *runtimeFakeAdapter) Execute(ctx context.Context, ex adapter.Execution) (*adapter.Result, error) {
	a.calls.Add(1)
	a.endpoint.Store(int32(len(ex.Endpoint)))
	body := io.NopCloser(bytes.NewReader(a.body))
	if a.streamErr {
		body = errAfterReadCloser{r: bytes.NewReader(a.body)}
	}
	return &adapter.Result{Status: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: body}, nil
}

func (a *runtimeFakeAdapter) Operate(ctx context.Context, op adapter.Operation) (adapter.OperationResult, error) {
	return adapter.OperationResult{}, nil
}

type errAfterReadCloser struct{ r *bytes.Reader }

func (e errAfterReadCloser) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err == io.EOF {
		return n, errors.New("stream broke")
	}
	return n, err
}
func (e errAfterReadCloser) Close() error { return nil }
