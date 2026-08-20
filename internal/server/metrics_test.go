package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Rethinger/2papi/internal/config"
	"github.com/Rethinger/2papi/internal/resilience"
	"github.com/Rethinger/2papi/internal/telemetry"
)

func TestMetricsEndpointExposition(t *testing.T) {
	snap, err := config.Build(config.Config{
		Version: 1,
		Secret:  "s",
		VirtualKeys: []config.VirtualKey{
			{Name: "all-key", Key: "sk-all", Models: []string{"*"}},
			{Name: "limited-key", Key: "sk-lim", Models: []string{"model-1"}},
		},
		Models: []config.Model{
			{Alias: "model-1", UpstreamModel: "up-1", Accounts: []string{"acct-1"}},
			{Alias: "model-2", UpstreamModel: "up-2", Accounts: []string{"acct-2"}},
		},
		Accounts: []config.Account{
			{Name: "acct-1", BaseURL: "http://fake1", APIKey: "k1", Enabled: true},
			{Name: "acct-2", BaseURL: "http://fake2", APIKey: "k2", Enabled: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	st := resilience.New()
	st.Failure("acct-2", 3) // Open circuit on acct-2
	gw := NewRuntimeServer(snap, st)
	h := gw.Routes()

	// 1. Observe some telemetry
	gw.metrics.Observe(telemetry.Event{
		PublicModel:    "model-1",
		FinalStatus:    200,
		Success:        true,
		InputTokens:    120,
		OutputTokens:   80,
		TotalLatencyMS: 45,
		Attempts:       []telemetry.Attempt{{Account: "acct-1", Adapter: "openai-compatible", Outcome: "success"}},
	})

	// 2. Fetch /metrics
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status=%d", rec.Code)
	}
	body := rec.Body.String()

	if !strings.Contains(body, "gateway_requests_total{model=\"model-1\",status=\"200\",outcome=\"success\"} 1") {
		t.Fatalf("requests counter missing: %s", body)
	}
	if !strings.Contains(body, "gateway_tokens_total{model=\"model-1\",direction=\"input\"} 120") {
		t.Fatalf("input tokens counter missing: %s", body)
	}
	if !strings.Contains(body, "gateway_tokens_total{model=\"model-1\",direction=\"output\"} 80") {
		t.Fatalf("output tokens counter missing: %s", body)
	}
	if !strings.Contains(body, "gateway_account_active_connections{account=\"acct-1\"} 0") {
		t.Fatalf("active connections gauge missing: %s", body)
	}
}

func TestModelsEndpointFiltersByVirtualKey(t *testing.T) {
	snap, err := config.Build(config.Config{
		Version: 1,
		Secret:  "s",
		VirtualKeys: []config.VirtualKey{
			{Name: "all-key", Key: "sk-all", Models: []string{"*"}},
			{Name: "limited-key", Key: "sk-lim", Models: []string{"model-1"}},
		},
		Models: []config.Model{
			{Alias: "model-1", UpstreamModel: "up-1", Accounts: []string{"acct-1"}},
			{Alias: "model-2", UpstreamModel: "up-2", Accounts: []string{"acct-2"}},
		},
		Accounts: []config.Account{
			{Name: "acct-1", BaseURL: "http://fake1", APIKey: "k1", Enabled: true},
			{Name: "acct-2", BaseURL: "http://fake2", APIKey: "k2", Enabled: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	gw := NewRuntimeServer(snap, resilience.New())
	h := gw.Routes()

	// 1. Unauthenticated request -> returns all models
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var allResp struct{ Data []map[string]any }
	_ = json.Unmarshal(rec.Body.Bytes(), &allResp)
	if len(allResp.Data) != 2 {
		t.Fatalf("unauthenticated models count=%d, want 2", len(allResp.Data))
	}

	// 2. Authenticated request with limited key -> only returns model-1
	req = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-lim")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var limResp struct{ Data []map[string]any }
	_ = json.Unmarshal(rec.Body.Bytes(), &limResp)
	if len(limResp.Data) != 1 || limResp.Data[0]["id"] != "model-1" {
		t.Fatalf("filtered models=%+v, want only model-1", limResp.Data)
	}
}

func TestEmbeddingsEndpointRouting(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}]}`)
	}))
	defer up.Close()

	snap, err := config.Build(config.Config{
		Version: 1,
		Secret:  "s",
		VirtualKeys: []config.VirtualKey{
			{Name: "vk", Key: "sk", Models: []string{"emb-model"}},
		},
		Models: []config.Model{
			{Alias: "emb-model", UpstreamModel: "text-embedding-3-small", Accounts: []string{"acct-1"}},
		},
		Accounts: []config.Account{
			{Name: "acct-1", BaseURL: up.URL, APIKey: "k1", Enabled: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	gw := NewRuntimeServer(snap, resilience.New())
	h := gw.Routes()

	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", bytes.NewReader([]byte(`{"model":"emb-model","input":"test string"}`)))
	req.Header.Set("Authorization", "Bearer sk")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("embeddings status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"embedding":[0.1,0.2]`) {
		t.Fatalf("unexpected embeddings body: %s", rec.Body.String())
	}
}

func TestMetricsLatencyAveragesExposition(t *testing.T) {
	snap, err := config.Build(config.Config{Version: 1, Secret: "s", VirtualKeys: []config.VirtualKey{{Name: "vk", Key: "sk", Models: []string{"m"}, RPM: 10}}, Models: []config.Model{{Alias: "m", UpstreamModel: "up", Accounts: []string{"a"}}}, Accounts: []config.Account{{Name: "a", BaseURL: "http://a", APIKey: "k", Enabled: true, Weight: 1, MaxConcurrency: 10}}, Resilience: config.Resilience{CircuitFailures: 1}})
	if err != nil {
		t.Fatal(err)
	}
	st := resilience.New()
	gw := NewRuntimeServer(snap, st)
	gw.metrics.Observe(telemetry.Event{PublicModel: "m", FinalStatus: http.StatusOK, Success: true, UpstreamTTFBMS: 40, OverheadMS: 8})
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	gw.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status=%d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `gateway_upstream_ms_avg{model="m"} 40`) {
		t.Fatalf("upstream avg missing: %s", body)
	}
	if !strings.Contains(body, `gateway_overhead_ms_avg{model="m"} 8`) {
		t.Fatalf("overhead avg missing: %s", body)
	}
}
