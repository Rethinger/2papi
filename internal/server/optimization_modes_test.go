package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Rethinger/2papi/internal/compression"
	"github.com/Rethinger/2papi/internal/config"
	"github.com/Rethinger/2papi/internal/resilience"
)

func optModesSnapshot(t *testing.T, upstreamURL string, opt config.Optimization) *config.Snapshot {
	t.Helper()
	snap, err := config.Build(config.Config{
		Version:      1,
		Secret:       "s",
		Optimization: opt,
		VirtualKeys:  []config.VirtualKey{{Name: "vk", Key: "sk", Models: []string{"chat"}, RPM: 100000}},
		Models:       []config.Model{{Alias: "chat", UpstreamModel: "u", Accounts: []string{"a"}}},
		Accounts:     []config.Account{{Name: "a", BaseURL: upstreamURL, APIKey: "k", Enabled: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func bigToolBody(minChars int) []byte {
	line := strings.Repeat("x", 40)
	var sb strings.Builder
	for len(sb.String()) < minChars {
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	return []byte(`{"model":"chat","messages":[{"role":"system","content":"You are helpful."},{"role":"user","content":"run tests"},{"role":"tool","content":` + jsonString(sb.String()) + `}]}`)
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestOptimizationModeEchoHeadersAndUpstreamRewrite(t *testing.T) {
	var upstreamBody []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		upstreamBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"1","choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer up.Close()

	gw := NewRuntimeServer(optModesSnapshot(t, up.URL, config.Optimization{
		RTKCompression: true,
		RTKMode:        compression.ModeAggressive,
		Caveman:        true,
		CavemanMode:    compression.ModeLite,
	}), resilience.New())
	h := gw.Routes()

	post := func(body []byte, headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer sk")
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// Aggressive preset from global config compresses a 2KB tool result.
	rec := post(bigToolBody(2048), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Gateway-RTK-Mode"); got != "aggressive" {
		t.Fatalf("rtk mode echo = %q", got)
	}
	if !strings.Contains(string(upstreamBody), "lines elided by gateway compression") {
		t.Fatalf("upstream body was not compressed")
	}

	// Caveman lite from config echoes its mode and injects the lite directive.
	rec = post([]byte(`{"model":"chat","messages":[{"role":"system","content":"sys"},{"role":"user","content":"hi"}]}`), nil)
	if got := rec.Header().Get("X-Gateway-Caveman-Mode"); got != "lite" {
		t.Fatalf("caveman mode echo = %q", got)
	}
	if !strings.Contains(string(upstreamBody), compression.CavemanLiteDirective[:40]) {
		t.Fatalf("lite directive missing upstream")
	}

	// Per-request header overrides the configured mode.
	rec = post(bigToolBody(2048), map[string]string{"X-Gateway-Compress": "light"})
	if got := rec.Header().Get("X-Gateway-RTK-Mode"); got != "" {
		t.Fatalf("light on a small body must not compress, but echoed %q", got)
	}
	if strings.Contains(string(upstreamBody), "lines elided by gateway compression") {
		t.Fatalf("light must not touch a 2KB body")
	}
	rec = post(bigToolBody(2048), map[string]string{"X-Gateway-Compress": "false"})
	if got := rec.Header().Get("X-Gateway-RTK-Mode"); got != "" {
		t.Fatalf("disabled RTK must not echo a mode, got %q", got)
	}
}
