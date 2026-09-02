package proxy_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Rethinger/2papi/internal/config"
	"github.com/Rethinger/2papi/internal/proxy"
	"github.com/Rethinger/2papi/internal/resilience"
	"github.com/Rethinger/2papi/internal/router"
	"github.com/Rethinger/2papi/internal/server"
)

func TestSquozeV2ProxyIntegration(t *testing.T) {
	var capturedUpstreamBody []byte

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r.Body)
		capturedUpstreamBody = buf.Bytes()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-test",
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "done"}}},
		})
	}))
	defer upstream.Close()

	cfg := config.Config{
		Version: 1,
		Secret:  "test-secret-squoze-v2-test",
		Optimization: config.Optimization{
			Squoze: true, // Enable Squoze v2 globally
		},
		VirtualKeys: []config.VirtualKey{
			{Name: "vk-test", Key: "sk-test", Models: []string{"gpt-4o"}},
		},
		Models: []config.Model{
			{
				Alias:         "gpt-4o",
				UpstreamModel: "gpt-4o",
				Accounts:      []string{"acc1"},
			},
		},
		Accounts: []config.Account{
			{
				Name:    "acc1",
				Adapter: "openai",
				BaseURL: upstream.URL,
				APIKey:  "dummy-key",
				Enabled: true,
			},
		},
	}

	snap, err := config.Build(cfg)
	if err != nil {
		t.Fatalf("failed to build config snapshot: %v", err)
	}

	st := resilience.New()
	rt := router.New(snap, st)
	px := proxy.New(snap, st, rt)
	srv := httptest.NewServer(server.New(snap, px).Routes())
	defer srv.Close()

	// Payload with repetitive tabular JSON in tool output
	type Record struct {
		ID     int    `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	var records []Record
	for i := 0; i < 25; i++ {
		records = append(records, Record{ID: 100 + i, Name: "worker-" + strconv.Itoa(i), Status: "HEALTHY"})
	}
	rawTable, _ := json.MarshalIndent(records, "", "  ")

	reqPayload := map[string]any{
		"model": "gpt-4o",
		"messages": []any{
			map[string]any{"role": "user", "content": "check cluster status"},
			map[string]any{"role": "tool", "tool_call_id": "call_1", "content": string(rawTable)},
		},
	}
	bodyBytes, _ := json.Marshal(reqPayload)

	req, _ := http.NewRequest("POST", srv.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-test")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: %d", resp.StatusCode)
	}

	// 1. Verify Squoze v2 headers
	if resp.Header.Get("X-Gateway-Squoze") != "true" {
		t.Fatalf("X-Gateway-Squoze header missing or not true: %q", resp.Header.Get("X-Gateway-Squoze"))
	}
	latencyHdr := resp.Header.Get("X-Gateway-Squoze-Latency-Ms")
	if latencyHdr == "" {
		t.Fatal("X-Gateway-Squoze-Latency-Ms header missing")
	}
	lat, err := strconv.ParseFloat(latencyHdr, 64)
	if err != nil || lat < 0 {
		t.Fatalf("invalid latency reported: %q", latencyHdr)
	}
	t.Logf("Squoze v2 proxy latency: %.2f ms", lat)

	transforms := resp.Header.Get("X-Gateway-Squoze-Transforms")
	if !strings.Contains(transforms, "squoze_v2_distiller") {
		t.Fatalf("expected squoze_v2_distiller in transforms header: %q", transforms)
	}

	savedBytes := resp.Header.Get("X-Gateway-Saved-Bytes")
	if savedBytes == "" || savedBytes == "0" {
		t.Fatalf("expected saved bytes for tabular lifting, got %q", savedBytes)
	}
	t.Logf("Squoze v2 saved bytes: %s", savedBytes)

	// 2. Verify upstream payload contains tabular representation instead of bulky JSON
	if !strings.Contains(string(capturedUpstreamBody), "[... squoze table: 25 rows ...]") {
		t.Fatalf("upstream body was not distilled into table:\n%s", string(capturedUpstreamBody))
	}
}
